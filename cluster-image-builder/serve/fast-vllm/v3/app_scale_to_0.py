# app_scale_to_0.py
import os
import sys
import time
import atexit
import json
import socket
import signal
import subprocess
from datetime import datetime
from typing import Optional, Set, Dict

import ray
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle
from fastapi import FastAPI, Request, Response
from fastapi.responses import StreamingResponse
import httpx

# ============================ 公共：日志 & 小工具 ============================

LOG_FILE = "/tmp/switchboard_scale0.log"

def sb_log(event: str, **kv):
    ts = datetime.now().strftime("%H:%M:%S.%f")[:-3]
    parts = [f"[{ts}]", f"[{event}]"] + [f"{k}={v}" for k, v in kv.items()]
    try:
        with open(LOG_FILE, "a", buffering=1) as f:
            f.write(" ".join(parts) + "\n")
    except Exception:
        pass

def find_free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("", 0))
    port = s.getsockname()[1]
    s.close()
    return port

def wait_port(host: str, port: int, timeout: float = 30.0, interval: float = 0.2) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection((host, port), timeout=1.0):
                return True
        except Exception:
            time.sleep(interval)
    return False

def wait_http_ok(url: str, timeout: float = 30.0, interval: float = 0.3) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with httpx.Client(timeout=2.0) as client:
                r = client.get(url)
                if r.status_code in (200, 204):
                    return True
        except Exception:
            time.sleep(interval)
    return False

def popen_with_logs(cmd: list[str], log_path: str, env: Optional[dict] = None) -> subprocess.Popen:
    """启动子进程，并把 stdout/stderr 追加写入到本地文件。"""
    os.makedirs(os.path.dirname(log_path), exist_ok=True)
    logf = open(log_path, "a", buffering=1)
    preexec = os.setsid if hasattr(os, "setsid") else None
    return subprocess.Popen(
        cmd,
        stdout=logf,
        stderr=logf,
        env=env or os.environ.copy(),
        preexec_fn=preexec,
        start_new_session=(preexec is None),  # Windows 兼容
    )

def kill_proc_tree(p: Optional[subprocess.Popen], sig=signal.SIGTERM):
    if not p:
        return
    try:
        if hasattr(os, "killpg") and p.pid:
            os.killpg(os.getpgid(p.pid), sig)
        else:
            p.terminate()
    except Exception:
        pass

# ============================ World：CPU 常驻 + 激活时占 GPU（scale to 0） ============================

@serve.deployment(
    name="WorldScale0",
    num_replicas=1,
    ray_actor_options={"num_gpus": 0},  # 关键：world 自身不占 GPU
)
class WorldScale0:
    """
    职责：
      - 常驻运行你提供的 server_v101.py（仅 CPU 阶段已完成，不持有显存）。
      - Hello 副本 0->1：调用 /internal/engine/activate 注入 CUDA_VISIBLE_DEVICES 激活（开始占用显存）。
      - Hello 副本 1->0：重启 server_v101.py 回到“未激活”状态，实现显存归零（scale to 0）。
      - 负责子进程日志、就绪探测与 SIGTERM/SIGINT 清理。
    """

    def __init__(
        self,
        # 基本模型/TP 配置（传给子进程的 env）
        model: str,
        tp: int = 1,
        max_len: int = 1024,
        dtype: str = "bfloat16",
        load_format: str = "tensorizer",
        tensorizer_uri: str = "",
        served_model_name: Optional[str] = None,

        # 进程与 API 配置
        log_path: str = "/tmp/world_scale0.log",
        port: int = 8011,
        admin_token: str = "supersecret",
        server_py: str = sys.executable,
        server_entry: str = "server_v101.py",  # 你的 patched 版本路径
        host: str = "127.0.0.1",

        # 超时与路径
        ready_timeout_s: float = 120.0,
    ):
        self.proc: Optional[subprocess.Popen] = None
        self.hello_live: Set[str] = set()
        self.activated: bool = False

        # 配置
        self.model = model
        self.tp = int(tp)
        self.max_len = int(max_len)
        self.dtype = dtype
        self.load_format = load_format
        self.tensorizer_uri = tensorizer_uri
        self.served_model_name = served_model_name or model

        self.log_path = log_path
        self.port = int(port)
        self.host = host
        self.base_url = f"http://{self.host}:{self.port}"
        self.admin_token = admin_token
        self.server_py = server_py
        self.server_entry = server_entry

        self.ready_timeout_s = float(ready_timeout_s)

        sb_log("world0_init",
               model=self.model, tp=self.tp, max_len=self.max_len,
               dtype=self.dtype, load_format=self.load_format,
               tensorizer_uri=self.tensorizer_uri, served=self.served_model_name,
               port=self.port, entry=self.server_entry)

        atexit.register(self._cleanup_proc)
        try:
            signal.signal(signal.SIGTERM, self._on_term)
            signal.signal(signal.SIGINT, self._on_term)
        except Exception:
            pass

        # 启动 CPU-only server 进程
        self._spawn_server_cpu_only()

    # ---- 子进程生命周期 ----

    def _spawn_server_cpu_only(self):
        """启动 server_v101.py：仅 CPU 阶段就绪，不占用显存。"""
        env = os.environ.copy()
        # 关键：删除 CUDA_VISIBLE_DEVICES，避免启动时触发任何 GPU 可见性
        env.pop("CUDA_VISIBLE_DEVICES", None)

        # 传递必要 env 给 server_v101.py
        env.update({
            "ADMIN_TOKEN": self.admin_token,
            "HOST": self.host,
            "PORT": str(self.port),
            "VLLM_MODEL": self.model,
            "SERVED_MODEL_NAME": self.served_model_name,
            "TP_SIZE": str(self.tp),
            "MAX_LEN": str(self.max_len),
            "VLLM_USE_V1": "1",
        })
        if self.dtype:
            env["DTYPE"] = self.dtype  # 你的 patched 里未直接读取，保留给未来需要
        if self.load_format:
            env["LOAD_FORMAT"] = self.load_format
        if self.tensorizer_uri:
            env["TENSORIZER_URI"] = self.tensorizer_uri  # 若你改成从 env 取，也可用

        cmd = [self.server_py, self.server_entry]
        self.proc = popen_with_logs(cmd, self.log_path, env=env)
        sb_log("world0_spawn", pid=(self.proc.pid if self.proc else None), cmd=" ".join(cmd), log=self.log_path)

        ok_port = wait_port(self.host, self.port, timeout=60.0)
        ok_health = False
        if ok_port:
            ok_health = wait_http_ok(f"{self.base_url}/healthz", timeout=self.ready_timeout_s)
        sb_log("world0_ready", port_ok=ok_port, health_ok=ok_health)

    def _cleanup_proc(self):
        try:
            if self.proc and (self.proc.poll() is None):
                sb_log("world0_cleanup_begin", pid=self.proc.pid)
                kill_proc_tree(self.proc, sig=signal.SIGTERM)
                time.sleep(1.5)
                if self.proc.poll() is None:
                    kill_proc_tree(self.proc, sig=signal.SIGKILL)
                sb_log("world0_cleanup_done")
        except Exception as e:
            sb_log("world0_cleanup_err", err=str(e))
        finally:
            self.proc = None
            self.activated = False

    def _on_term(self, signum, frame):
        sb_log("world0_sigterm", signum=signum)
        self._cleanup_proc()

    # ---- 激活 / 归零（通过 HTTP API） ----

    def _activate_with_devices(self, devices: str) -> bool:
        """调用 /internal/engine/activate，传入 cuda_visible_devices。"""
        url = f"{self.base_url}/internal/engine/activate"
        headers = {"x-admin-token": self.admin_token}
        try:
            with httpx.Client(timeout=120.0) as client:
                r = client.post(url, headers=headers, params={"cuda_visible_devices": devices})
                ok = (r.status_code == 200)
                sb_log("world0_activate_call", status=r.status_code, body=(r.text[:200] if r.text else ""))
                if not ok:
                    return False
        except Exception as e:
            sb_log("world0_activate_exc", err=str(e))
            return False

        # 等 /v1/models 就绪
        models_ok = wait_http_ok(f"{self.base_url}/v1/models", timeout=self.ready_timeout_s)
        self.activated = models_ok
        sb_log("world0_activate_models_ok", ok=models_ok)
        return models_ok

    def _deactivate_to_cpu(self):
        """
        将显存归零：当前最简单可靠的方式是重启 server 进程（回到 CPU-only 状态）。
        若你的 server_v101.py 未来提供 /internal/engine/deactivate，可改为 HTTP 调用。
        """
        self._cleanup_proc()
        # 立刻拉起“未激活”的 CPU-only 进程
        self._spawn_server_cpu_only()

    # ---- 与 Hello 的事件交互 ----

    async def notify_hello_start(self, rid: str, devices: str) -> dict:
        before = len(self.hello_live)
        self.hello_live.add(rid)
        after = len(self.hello_live)
        sb_log("hello0_start", rid=rid, before=before, after=after, devices=devices)

        activated = False
        if before == 0 and after == 1:
            # 第一个 Hello 上线 → 激活占用显存
            activated = self._activate_with_devices(devices)

        return {
            "count": after,
            "activated": activated,
            "world_port": self.port,
            "world_host": self.host,
        }

    async def notify_hello_stop(self, rid: str) -> dict:
        before = len(self.hello_live)
        self.hello_live.discard(rid)
        after = len(self.hello_live)
        sb_log("hello0_stop", rid=rid, before=before, after=after)

        if before > 0 and after == 0:
            # 最后一个 Hello 下线 → 显存归零（重启成 CPU-only）
            self._deactivate_to_cpu()

        return {"count": after, "activated": self.activated}

    async def get_status(self) -> dict:
        alive = (self.proc is not None and self.proc.poll() is None)
        return {
            "hello_count": len(self.hello_live),
            "vllm_proc_alive": alive,
            "activated": self.activated,
            "port": self.port,
            "pid": (self.proc.pid if alive else None),
        }

# ============================ Hello：统一代理 /v1/chat/completions（始终走 World） ============================

hello_app = FastAPI()

_WORLD_HOST: Optional[str] = None
_WORLD_PORT: Optional[int] = None

@hello_app.get("/")
def root():
    # 模拟少量负载，便于触发扩缩容（可删）
    time.sleep(0.5)
    return "hello scale-0 ok"

def _gpu_env_snapshot() -> dict:
    return {
        "CUDA_VISIBLE_DEVICES": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
        "NVIDIA_VISIBLE_DEVICES": os.environ.get("NVIDIA_VISIBLE_DEVICES", ""),
        "CUDA_DEVICE_ORDER": os.environ.get("CUDA_DEVICE_ORDER", ""),
        "pid": os.getpid(),
    }

@hello_app.on_event("startup")
async def _startup():
    ctx = serve.get_replica_context()
    rid = ctx.replica_tag
    env = _gpu_env_snapshot()
    sb_log("hello0_startup_gpu", rid=rid, **env)

    # 将 Hello 实际获得的设备列表上报给 World，用于 World 激活到相同物理卡
    devices = os.environ.get("CUDA_VISIBLE_DEVICES", "")

    h: DeploymentHandle = serve.get_deployment_handle("WorldScale0")
    res = await h.notify_hello_start.remote(rid, devices)

    global _WORLD_HOST, _WORLD_PORT
    _WORLD_HOST = res.get("world_host", "127.0.0.1")
    _WORLD_PORT = int(res.get("world_port", 8011))

@hello_app.on_event("shutdown")
async def _shutdown():
    ctx = serve.get_replica_context()
    rid = ctx.replica_tag
    try:
        h = serve.get_deployment_handle("WorldScale0")
        await h.notify_hello_stop.remote(rid)
    except Exception:
        pass

# 统一代理：非流式 & 流式（始终转发给 WorldScale0 子进程暴露的 OpenAI 端口）
@hello_app.post("/v1/chat/completions")
async def chat_completions(req: Request):
    try:
        payload = await req.json()
    except Exception:
        return Response(content=json.dumps({"error": "invalid JSON"}), media_type="application/json", status_code=400)

    if not _WORLD_PORT or not _WORLD_HOST:
        return Response(content=json.dumps({"error": "world not ready"}), media_type="application/json", status_code=503)

    headers = {"Content-Type": "application/json"}
    auth = req.headers.get("authorization")
    if auth:
        headers["Authorization"] = auth

    url = f"http://{_WORLD_HOST}:{int(_WORLD_PORT)}/v1/chat/completions"
    is_stream = bool(payload.get("stream"))

    # 流式
    if is_stream:
        sb_log("proxy0_chat_stream_begin", host=_WORLD_HOST, port=_WORLD_PORT)

        async def _iter_upstream():
            try:
                async with httpx.AsyncClient(timeout=None) as client:
                    async with client.stream("POST", url, headers=headers, json=payload) as r:
                        status = r.status_code
                        if status != 200:
                            text = await r.aread()
                            sb_log("proxy0_chat_stream_err", status=status, body=text[:200] if text else "")
                            # 以非流式错误返回（注意：流式上下文不能直接 return，这里按 SSE error 事件结束）
                            yield f'data: {json.dumps({"error": f"upstream status {status}"})}\n\n'.encode("utf-8")
                            return
                        async for chunk in r.aiter_raw():
                            if chunk:
                                yield chunk
            except Exception as e:
                sb_log("proxy0_chat_stream_exc", err=str(e))
                yield b'data: {"error":"stream aborted"}\n\n'

        return StreamingResponse(_iter_upstream(), media_type="text/event-stream")

    # 非流式
    try:
        async with httpx.AsyncClient(timeout=120.0) as client:
            r = await client.post(url, headers=headers, json=payload)
            status, body = r.status_code, r.text
    except Exception as e:
        sb_log("proxy0_chat_exc", host=_WORLD_HOST, port=_WORLD_PORT, err=str(e))
        return Response(content=json.dumps({"error": "backend unreachable"}), media_type="application/json", status_code=502)

    sb_log("proxy0_chat", host=_WORLD_HOST, port=_WORLD_PORT, status=status)
    return Response(content=body, media_type="application/json", status_code=int(status))

@serve.deployment(
    name="HelloScale0",
    ray_actor_options={"num_gpus": "auto"},  # 由 Ray 实际分配（单副本场景）
    num_replicas="auto",
    autoscaling_config={
        "min_replicas": 0,
        "max_replicas": 1,
        "target_num_ongoing_requests_per_replica": 1,
        "upscale_delay_s": 1,
        "downscale_delay_s": 8,
        "metrics_interval_s": 1,
        "look_back_period_s": 1,
    },
)
@serve.ingress(hello_app)
class HelloScale0:
    """
    统一对外提供 /v1/chat/completions（支持 stream），
    启停时通过 WorldScale0 完成显存“占用/归零”的切换。
    """
    def __init__(self, world: DeploymentHandle):
        self.world = world

# ============================ App Builder（统一参数入口） ============================

def app_builder(args: Dict[str, str]) -> Application:
    # —— 模型参数 ——
    model = args.get("model", "Qwen/Qwen3-14B")
    tp = int(args.get("tp", 4))
    max_len = int(args.get("max_len", 4096))
    dtype = args.get("dtype", "bfloat16")
    load_format = args.get("load_format", "tensorizer")
    tensorizer_uri = args.get("tensorizer_uri", "/home/ec2-user/image-builder-test/output/vllm/Qwen/Qwen3-14B/v1/model-rank-%03d.tensors")
    served_model_name = args.get("served_model_name", model)

    # —— World 进程参数 ——
    world_log = args.get("world_log", "/tmp/world_scale0.log")
    world_port = int(args.get("world_port", 8011))
    world_host = args.get("world_host", "127.0.0.1")  # 单节点先用 loopback
    admin_token = args.get("admin_token", "supersecret")
    server_py = args.get("world_python_bin", sys.executable)
    server_entry = args.get("world_server_entry", "server_v101.py")
    ready_timeout_s = float(args.get("world_ready_timeout_s", 180.0))

    world = WorldScale0.bind(
        model=model,
        tp=tp,
        max_len=max_len,
        dtype=dtype,
        load_format=load_format,
        tensorizer_uri=tensorizer_uri,
        served_model_name=served_model_name,
        log_path=world_log,
        port=world_port,
        admin_token=admin_token,
        server_py=server_py,
        server_entry=server_entry,
        host=world_host,
        ready_timeout_s=ready_timeout_s,
    )

    # 最终应用入口：Hello（依赖 World）
    hello = HelloScale0.bind(world)
    return hello

graph = app_builder({})