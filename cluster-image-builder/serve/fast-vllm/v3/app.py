# app.py
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

LOG_FILE = "/tmp/switchboard.log"

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

def wait_port(host: str, port: int, timeout: float = 20.0, interval: float = 0.2) -> bool:
    """等待 TCP 端口开始监听。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection((host, port), timeout=1.0):
                return True
        except Exception:
            time.sleep(interval)
    return False

def wait_http_ok(url: str, timeout: float = 20.0, interval: float = 0.3) -> bool:
    """等待 HTTP 200/204（最小实现）。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with httpx.Client(timeout=1.0) as client:
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

# ============================ World：全局 vLLM（sleep 模式，常驻 1 副本） ============================

@serve.deployment(
    name="World",
    num_replicas=1,
    ray_actor_options={"num_gpus": 0},
)
class World:
    """
    职责：
      - 持有全局 sleep-mode vLLM（进程管理 + 就绪探测）。
      - Hello 副本 0->1：唤醒；1->0：休眠。
      - 本部署关闭/重启时，负责回收 vLLM 进程。
    """
    def __init__(
        self,
        model: str,
        gpu_memory_util: str,
        log_path: str = "/tmp/vllm_world.log",
        port: int = 8001,
        wake_path: str = "/wake_up",
        sleep_path: str = "/sleep?level=1",
        python_bin: str = sys.executable,
        extra_args: str = "",
        tp: int = 1,
    ):
        self.hello_live: Set[str] = set()
        self.sleeping: bool = True
        self.proc: Optional[subprocess.Popen] = None

        # 参数来自 app_builder
        self.model = model
        self.gpu_mem_util = gpu_memory_util
        self.log_path = log_path
        self.port = int(port)
        self.base_url = f"http://127.0.0.1:{self.port}"
        self.wake_path = wake_path
        self.sleep_path = sleep_path
        self.py_bin = python_bin
        self.extra_args = extra_args
        self.tp = tp

        sb_log("world_init",
               sleeping=self.sleeping,
               port=self.port,
               model=self.model,
               gpu_mem_util=self.gpu_mem_util,
               wake_path=self.wake_path,
               sleep_path=self.sleep_path)

        atexit.register(self._cleanup_world_vllm)
        try:
            signal.signal(signal.SIGTERM, self._on_term)
            signal.signal(signal.SIGINT, self._on_term)
        except Exception:
            pass

        # 启动即确保 vLLM 存在并进入 sleep（初始“热而睡”）
        self._ensure_vllm_started()
        self._sleep()
        self.sleeping = True

    # ---- 进程控制 ----

    def _ensure_vllm_started(self):
        if self.proc and (self.proc.poll() is None):
            return
        env = os.environ.copy()
        env["VLLM_SERVER_DEV_MODE"] = "1"
        env.pop("CUDA_VISIBLE_DEVICES", None)
        env["PYTHONPATH"] = "/home/ec2-user/image-builder-test/serve/fast-vllm/v3/"

        cmd = [
            self.py_bin, "-m", "vllm.entrypoints.openai.api_server",
            "--model", self.model,
            "--enable-sleep-mode",
            "--gpu-memory-utilization", str(self.gpu_mem_util),
            "--port", str(self.port),
            "--tensor-parallel-size", str(self.tp),
            "--max-model-len", "1024",
        ]
        if self.extra_args.strip():
            cmd.extend(self.extra_args.strip().split())

        self.proc = popen_with_logs(cmd, self.log_path, env=env)
        sb_log("world_vllm_spawn", pid=(self.proc.pid if self.proc else None), port=self.port, cmd=" ".join(cmd), log=self.log_path)

        ok_port = wait_port("127.0.0.1", self.port, timeout=6000.0, interval=0.2)
        ok_models = False
        if ok_port:
            # 检查 /v1/models API 返回 200，确认模型已加载
            models_url = f"http://127.0.0.1:{self.port}/v1/models"
            deadline = time.time() + 600.0
            while time.time() < deadline:
                try:
                    with httpx.Client(timeout=3.0) as client:
                        r = client.get(models_url)
                        if r.status_code == 200:
                            ok_models = True
                            break
                except Exception:
                    time.sleep(0.5)
        sb_log("world_vllm_ready", port_ok=ok_port, models_ok=ok_models)

    def _wake(self):
        url = self.base_url + self.wake_path
        try:
            r = httpx.post(url, timeout=60.0)
            code, body = r.status_code, r.text
        except Exception as e:
            code, body = -1, str(e)
        sb_log("world_vllm_wake", url=url, status=code, body=(body[:200] if body else ""))

    def _sleep(self):
        url = self.base_url + self.sleep_path
        try:
            r = httpx.post(url, timeout=60.0)
            code, body = r.status_code, r.text
        except Exception as e:
            code, body = -1, str(e)
        sb_log("world_vllm_sleep", url=url, status=code, body=(body[:200] if body else ""))

    def _cleanup_world_vllm(self):
        try:
            if self.proc and (self.proc.poll() is None):
                sb_log("world_vllm_cleanup_begin", pid=self.proc.pid)
                kill_proc_tree(self.proc, sig=signal.SIGTERM)
                time.sleep(0.5)
                if self.proc.poll() is None:
                    kill_proc_tree(self.proc, sig=signal.SIGKILL)
                sb_log("world_vllm_cleanup_done")
        except Exception as e:
            sb_log("world_vllm_cleanup_err", err=str(e))
        finally:
            self.proc = None

    def _on_term(self, signum, frame):
        sb_log("world_sigterm", signum=signum)
        self._cleanup_world_vllm()

    # ---- 从 Hello 接收事件，并返回是否需要本地 vLLM ----

    async def notify_hello_start(self, rid: str) -> dict:
        before = len(self.hello_live)
        self.hello_live.add(rid)
        after = len(self.hello_live)
        sb_log("hello_start", rid=rid, before=before, after=after)

        launch_local = after > 1
        if before == 0 and after == 1:
            self._ensure_vllm_started()
            self._wake()
            self.sleeping = False

        return {"count": after, "launch_local": launch_local, "world_port": self.port}

    async def notify_hello_stop(self, rid: str) -> dict:
        before = len(self.hello_live)
        self.hello_live.discard(rid)
        after = len(self.hello_live)
        sb_log("hello_stop", rid=rid, before=before, after=after)

        if before > 0 and after == 0:
            self._sleep()
            self.sleeping = True

        return {"count": after}

    async def get_status(self) -> dict:
        alive = (self.proc is not None and self.proc.poll() is None)
        return {
            "sleeping": self.sleeping,
            "hello_count": len(self.hello_live),
            "vllm_alive": alive,
            "port": self.port,
            "pid": (self.proc.pid if alive else None),
        }

# ============================ Hello：本地 vLLM（>1 副本时本地起），统一代理 /v1/chat/completions ============================

# 模块级：Hello 副本所在进程的「本地 vLLM」状态与配置（由 app_builder 注入）
_local_proc: Optional[subprocess.Popen] = None
_local_port: Optional[int] = None
_use_local: bool = False         # 由 startup 决定
_world_port: Optional[int] = None
_cleanup_registered: bool = False

_HELLO_MODEL = ""
_HELLO_PYBIN = sys.executable
_HELLO_EXTRA = ""
_HELLO_GPU_UTIL = ""
_HELLO_LOG = "/tmp/vllm_hello.log"
_HELLO_TP = 1

def _cleanup_local_vllm():
    global _local_proc, _local_port
    try:
        if _local_proc and (_local_proc.poll() is None):
            sb_log("hello_vllm_cleanup_begin", pid=_local_proc.pid)
            kill_proc_tree(_local_proc, sig=signal.SIGTERM)
            time.sleep(0.5)
            if _local_proc and (_local_proc.poll() is None):
                kill_proc_tree(_local_proc, sig=signal.SIGKILL)
            sb_log("hello_vllm_cleanup_done")
    except Exception as e:
        sb_log("hello_vllm_cleanup_err", err=str(e))
    finally:
        _local_proc = None
        _local_port = None

def _register_cleanup_once():
    global _cleanup_registered
    if _cleanup_registered:
        return
    atexit.register(_cleanup_local_vllm)
    try:
        signal.signal(signal.SIGTERM, lambda *a, **k: _cleanup_local_vllm())
        signal.signal(signal.SIGINT,  lambda *a, **k: _cleanup_local_vllm())
    except Exception:
        pass
    _cleanup_registered = True

def _start_local_vllm():
    """在本副本进程内启动普通模式 vLLM。"""
    global _local_proc, _local_port
    _register_cleanup_once()
    if _local_proc and (_local_proc.poll() is None):
        return
    port = find_free_port()
    cmd = [
        _HELLO_PYBIN, "-m", "vllm.entrypoints.openai.api_server",
        "--model", _HELLO_MODEL,
        "--gpu-memory-utilization", str(_HELLO_GPU_UTIL),
        "--port", str(port),
        "--tensor-parallel-size", str(_HELLO_TP),
        "--max-model-len", "4096",
    ]
    if _HELLO_EXTRA.strip():
        cmd.extend(_HELLO_EXTRA.strip().split())
    _local_proc = popen_with_logs(cmd, _HELLO_LOG)
    _local_port = port
    sb_log("hello_vllm_spawn", pid=_local_proc.pid, port=port, cmd=" ".join(cmd), log=_HELLO_LOG)

    ok_port = wait_port("127.0.0.1", port, timeout=600.0, interval=0.2)
    ok_models = False
    if ok_port:
        # 检查 /v1/models API 返回 200，确认模型已加载
        models_url = f"http://127.0.0.1:{port}/v1/models"
        deadline = time.time() + 600.0
        while time.time() < deadline:
            try:
                with httpx.Client(timeout=3.0) as client:
                    r = client.get(models_url)
                    if r.status_code == 200:
                        ok_models = True
                        break
            except Exception:
                time.sleep(0.5)
    sb_log("hello_vllm_ready", port_ok=ok_port, models_ok=ok_models, port=port)

def _stop_local_vllm():
    _cleanup_local_vllm()

hello_app = FastAPI()

def _gpu_env_snapshot() -> dict:
    return {
        "CUDA_VISIBLE_DEVICES": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
        "NVIDIA_VISIBLE_DEVICES": os.environ.get("NVIDIA_VISIBLE_DEVICES", ""),
        "CUDA_DEVICE_ORDER": os.environ.get("CUDA_DEVICE_ORDER", ""),
        "pid": os.getpid(),
    }

@hello_app.get("/")
def root():
    time.sleep(1)  # 模拟负载，便于触发扩缩容
    return "hello ok"

@hello_app.on_event("startup")
async def _startup():
    ctx = serve.get_replica_context()
    rid = ctx.replica_tag
    sb_log("hello_startup_gpu", rid=rid, **_gpu_env_snapshot())

    h = serve.get_deployment_handle("World")
    res = await h.notify_hello_start.remote(rid)

    # 记录路由所需状态（模块级）
    global _use_local, _world_port
    _world_port = int(res.get("world_port", 8001))
    _use_local = bool(res.get("launch_local"))

    if _use_local:
        _start_local_vllm()
        sb_log("hello_local_vllm_started", rid=rid, port=_local_port)
    else:
        sb_log("hello_local_vllm_skip", rid=rid)

@hello_app.on_event("shutdown")
async def _shutdown():
    ctx = serve.get_replica_context()
    rid = ctx.replica_tag
    try:
        if _local_proc is not None:
            _stop_local_vllm()
            sb_log("hello_local_vllm_stopped", rid=rid)
    except Exception:
        pass
    try:
        h = serve.get_deployment_handle("World")
        await h.notify_hello_stop.remote(rid)
    except Exception:
        pass

# 统一代理：非流式 & 流式
@hello_app.post("/v1/chat/completions")
async def chat_completions(req: Request):
    try:
        payload = await req.json()
    except Exception:
        return Response(content=json.dumps({"error": "invalid JSON"}), media_type="application/json", status_code=400)

    # 选择目标端口（优先本地）
    mode = "world"
    target_port: Optional[int] = None
    if _use_local and _local_port:
        target_port = _local_port
        mode = "local"
    else:
        target_port = _world_port

    if not target_port:
        return Response(content=json.dumps({"error": "no backend available"}), media_type="application/json", status_code=503)

    # 透传 Authorization（如有）
    headers = {"Content-Type": "application/json"}
    auth = req.headers.get("authorization")
    if auth:
        headers["Authorization"] = auth

    url = f"http://127.0.0.1:{int(target_port)}/v1/chat/completions"
    is_stream = bool(payload.get("stream"))

    # ---------- 流式 ----------
    if is_stream:
        sb_log("proxy_chat_stream_begin", mode=mode, port=target_port)

        async def _iter_upstream():
            # 直接把上游的 SSE 字节流透传
            try:
                async with httpx.AsyncClient(timeout=None) as client:
                    async with client.stream("POST", url, headers=headers, json=payload) as r:
                        status = r.status_code
                        if status != 200:
                            text = await r.aread()
                            sb_log("proxy_chat_stream_err", status=status, body=text[:200] if text else "")
                            # 以非流式错误返回
                            yield json.dumps({"error": f"upstream status {status}"}).encode("utf-8")
                            return
                        async for chunk in r.aiter_raw():
                            if chunk:
                                yield chunk
            except Exception as e:
                # SSE 连接中断等
                sb_log("proxy_chat_stream_exc", err=str(e))
                # 按 OpenAI SSE 规范，发送一个 error 事件也可，这里直接结束流
                yield b'data: {"error":"stream aborted"}\n\n'

        return StreamingResponse(_iter_upstream(), media_type="text/event-stream")

    # ---------- 非流式 ----------
    try:
        async with httpx.AsyncClient(timeout=60.0) as client:
            r = await client.post(url, headers=headers, json=payload)
            status, body = r.status_code, r.text
    except Exception as e:
        sb_log("proxy_chat_exc", mode=mode, port=target_port, err=str(e))
        return Response(content=json.dumps({"error": "backend unreachable"}), media_type="application/json", status_code=502)

    sb_log("proxy_chat", mode=mode, port=target_port, status=status)
    return Response(content=body, media_type="application/json", status_code=int(status))

@serve.deployment(
    name="Hello",
    ray_actor_options={"num_gpus": 4},
    num_replicas="auto",
    autoscaling_config={
        "min_replicas": 0,
        "max_replicas": 1,
        "target_num_ongoing_requests_per_replica": 1,
        "upscale_delay_s": 2,
        "downscale_delay_s": 10,
        "metrics_interval_s": 1,
        "look_back_period_s": 1,
    },
)
@serve.ingress(hello_app)
class Hello:
    """
    >1 副本时，每个 Hello 副本负责启动/销毁 **本地普通模式 vLLM**，
    并统一对外提供 /v1/chat/completions 代理（支持 stream）。
    """
    def __init__(self, world: DeploymentHandle):
        self.world = world

# ============================ App Builder（统一参数入口） ============================

def app_builder(args: Dict[str, str]) -> Application:
    model = args.get("model", "Qwen/Qwen3-14B")
    gpu_mem = args.get("gpu_memory_util", "0.4")
    tp = int(args.get("tp", 2))

    # —— 配置 World —— 
    world_log = args.get("world_log", "/tmp/vllm_world.log")
    world_port = int(args.get("world_port", 8001))
    world_wake_path = args.get("world_wake_path", "/wake_up")
    world_sleep_path = args.get("world_sleep_path", "/sleep?level=1")
    world_py = args.get("world_python_bin", sys.executable)
    world_extra = args.get("world_extra_args", "")

    world = World.bind(
        model,
        gpu_mem,
        world_log,
        world_port,
        world_wake_path,
        world_sleep_path,
        world_py,
        world_extra,
        tp,
    )

    # —— 配置 Hello（模块级变量） ——
    global _HELLO_MODEL, _HELLO_GPU_UTIL, _HELLO_LOG, _HELLO_PYBIN, _HELLO_EXTRA, _HELLO_TP
    _HELLO_MODEL = model
    _HELLO_GPU_UTIL = gpu_mem
    _HELLO_LOG = args.get("hello_log", "/tmp/vllm_hello.log")
    _HELLO_PYBIN = args.get("hello_python_bin", sys.executable)
    _HELLO_EXTRA = args.get("hello_extra_args", "")
    _HELLO_TP = tp

    # 最终应用入口：Hello（依赖 World）
    hello = Hello.bind(world)
    return hello

graph = app_builder({})