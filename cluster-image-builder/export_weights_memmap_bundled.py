#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
一体化权重导出脚本：将模型参数/缓冲按“bundle（通常按层号+dtype）”连续落盘，
以便 import 端按 bundle 一次 H2D，再在 GPU 端切片绑定，显著减少小块拷贝与同步。

环境变量（均可选）：
  MM_STORE_PATH=/dev/shm/weights.store
  MM_INDEX_PATH=/dev/shm/weights.index.pkl
  MM_LOCK_PATH=/dev/shm/weights.lock
  MM_ALIGN_BYTES=4096              # 文件起始对齐
  BUNDLE_ALIGN_BYTES=4096          # 每个 bundle 起始对齐（建议 >= 4096；THP 可用 2<<20）
  MEMBER_ALIGN_BYTES=4096          # bundle 内每个成员对齐

  EXPORT_DTYPE=keep|bfloat16|float16|float32  # 统一导出 dtype；keep 表示保留各自 dtype
  EXPORT_INCLUDE_BUFFERS=1|0
  EXPORT_PREALLOC_GB=0             # 预分配文件大小（GiB）；0=自动估算
  EXPORT_SKIP_PATTERNS="cos_sin_cache,.*rotary.*cache.*"
  EXPORT_GROUP_STRATEGY=layer|prefix|none
  EXPORT_ORDER=size|none           # 组内排序：按大小降序或不排序
  LOAD_WITH=hf|vllm                # 模型加载后导出；默认 hf（CPU）

运行：
  python export_weights_memmap_bundled.py Qwen/Qwen3-14B
"""

import os, re, sys, time, gc, pickle, fcntl
import mmap, ctypes
import numpy as np
import torch
from typing import Dict, Any, List, Tuple, Optional

# -------------------- 配置 --------------------
STORE = os.environ.get("MM_STORE_PATH",  "/dev/shm/weights.store")
INDEX = os.environ.get("MM_INDEX_PATH",  "/dev/shm/weights.index.pkl")
LOCK  = os.environ.get("MM_LOCK_PATH",   "/dev/shm/weights.lock")

ALIGN_FILE   = int(os.environ.get("MM_ALIGN_BYTES",       "4096"))
ALIGN_BUNDLE = int(os.environ.get("BUNDLE_ALIGN_BYTES",   "4096"))
ALIGN_MEMBER = int(os.environ.get("MEMBER_ALIGN_BYTES",   "4096"))

TARGET_EXPORT_DTYPE = os.environ.get("EXPORT_DTYPE", "keep").strip().lower()
INCLUDE_BUFFERS     = os.environ.get("EXPORT_INCLUDE_BUFFERS", "1") == "1"
PREALLOC_SIZE_GB    = float(os.environ.get("EXPORT_PREALLOC_GB", "0"))

_default_skips = [r"cos_sin_cache", r".*rotary.*cache.*"]
SKIP_PATTERNS = [p for p in os.environ.get("EXPORT_SKIP_PATTERNS", ",".join(_default_skips)).split(",") if p]
GROUP_STRATEGY = os.environ.get("EXPORT_GROUP_STRATEGY", "layer")  # layer|prefix|none
ORDER_IN_GROUP = os.environ.get("EXPORT_ORDER", "size")            # size|none

LOAD_WITH = os.environ.get("LOAD_WITH", "hf").strip().lower()      # hf|vllm

# -------------------- 工具函数 --------------------
def _align(x: int, k: int) -> int:
    return (x + (k - 1)) // k * k

def _ensure_parent(p: str):
    d = os.path.dirname(p) or "."
    os.makedirs(d, exist_ok=True)

def _load_index() -> Dict[str, Any]:
    if not os.path.exists(INDEX):
        return {}
    with open(INDEX, "rb") as f:
        return pickle.load(f)

def _save_index(idx: Dict[str, Any]) -> None:
    _ensure_parent(INDEX)
    tmp = INDEX + ".tmp"
    with open(tmp, "wb") as f:
        pickle.dump(idx, f, protocol=pickle.HIGHEST_PROTOCOL)
    os.replace(tmp, INDEX)

def _should_skip(name: str, is_buffer: bool) -> bool:
    for pat in SKIP_PATTERNS:
        if re.search(pat, name):
            return True
    return False

def _to_target_dtype(t: torch.Tensor) -> torch.Tensor:
    if TARGET_EXPORT_DTYPE == "keep":
        return t
    dt = getattr(torch, TARGET_EXPORT_DTYPE)
    return t.to(dtype=dt)

def _storage_view_numpy(t: torch.Tensor) -> Tuple[np.ndarray, str]:
    """
    返回 (np_array_view, storage_dtype_str)
    - bf16 以 uint16 位模式落盘，便于 import 端 frombuffer(dtype=torch.bfloat16) 0-copy 解释
    - 其他 dtype 直接用对应 numpy dtype
    """
    t = t.detach().contiguous()
    if t.device.type != "cpu":
        t = t.cpu()
    if t.dtype == torch.bfloat16:
        arr = t.numpy().view(np.uint16)   # 零拷贝 reinterpret
        return arr, "uint16"
    else:
        arr = t.numpy()                   # 零拷贝 view
        return arr, str(arr.dtype)

def _torch_elem_size(dt: torch.dtype) -> int:
    return torch.tensor([], dtype=dt).element_size()

def _np_itemsize_of_storage(storage_dtype: str) -> int:
    if storage_dtype == "uint16":
        return 2
    return np.dtype(storage_dtype).itemsize

def _preallocate_store(size_bytes: int):
    if os.path.exists(STORE) and os.path.getsize(STORE) >= size_bytes:
        return
    print(f"[export] Preallocating {size_bytes / (1024**3):.2f} GiB...")
    _ensure_parent(STORE)
    try:
        import subprocess
        subprocess.run(["fallocate", "-l", str(size_bytes), STORE], check=True, capture_output=True)
        print("[export] Preallocated with fallocate")
    except Exception:
        with open(STORE, "wb") as f:
            f.truncate(size_bytes)
        print("[export] Preallocated with truncate")

def _estimate_bytes_bundled(groups: Dict[str, List[Tuple[str, torch.Tensor]]]) -> int:
    """粗估总大小（含对齐）"""
    total = 0
    off = 0
    for gname, items in groups.items():
        off = _align(off, ALIGN_BUNDLE)
        rel = 0
        for _, t in items:
            tt = _to_target_dtype(t)
            arr, sd = _storage_view_numpy(tt)
            nbytes = arr.size * arr.itemsize
            rel = _align(rel, ALIGN_MEMBER)
            rel += nbytes
        total = off + rel
        off = total
    # 文件起始对齐
    total = _align(total, ALIGN_FILE)
    # 再加 5% 余量
    return int(total * 1.05)

def _group_key(name: str) -> str:
    if GROUP_STRATEGY == "none":
        return "all"
    if GROUP_STRATEGY == "prefix":
        return name.split(".", 1)[0]
    # 默认：按层号
    m = re.search(r"\.(\d+)\.", name)
    if m:
        return f"layer_{int(m.group(1))}"
    if "embed" in name:
        return "embeddings"
    if "lm_head" in name:
        return "lm_head"
    return "others"

def _dtype_key(t: torch.Tensor) -> str:
    """用于在组内继续按 dtype 二次分组，保证 bundle 内 dtype 统一"""
    if TARGET_EXPORT_DTYPE == "keep":
        return str(t.dtype)
    return TARGET_EXPORT_DTYPE

def prewarm_store(touch_bytes: int = 0) -> int:
    """预热 OS 页缓存：madvise WILLNEED；可选顺序触摸所有页"""
    touched = 0
    if not os.path.exists(STORE):
        return 0
    fd = os.open(STORE, os.O_RDONLY)
    try:
        mm = mmap.mmap(fd, 0, access=mmap.ACCESS_READ)
        try:
            # Python 3.11+ 支持 madvise
            if hasattr(mm, "madvise"):
                try:
                    mm.madvise(mmap.MADV_WILLNEED)
                    mm.madvise(mmap.MADV_SEQUENTIAL)
                except Exception:
                    pass
            if touch_bytes:
                step = 4096
                size = min(len(mm), touch_bytes)
                for i in range(0, size, step):
                    _ = mm[i]
                touched = size
        finally:
            mm.close()
    finally:
        os.close(fd)
    return touched

# -------------------- 导出会话（一次锁 + 复用 fd + 聚合索引） --------------------
def _pwrite_all(fd: int, mv: memoryview, offset: int, chunk: int = 256 * 1024 * 1024) -> None:
    """像 write(2) 一样，确保把 mv 全部写入 fd（支持超大内存块）。
    默认 256MiB 一块；你也可以用 1<<30 调大到 1GiB。"""
    total = len(mv)
    written = 0
    while written < total:
        end = written + chunk
        try:
            n = os.pwrite(fd, mv[written:end], offset + written)
        except InterruptedError:
            continue  # 重试
        if n is None:
            # 某些 Python 版本可能返回 None（极少见），防御性处理
            n = 0
        if n <= 0:
            raise OSError(f"pwrite stalled at {written}/{total} bytes")
        written += n

class ExportSession:
    def __init__(self):
        self.lock_fd: Optional[int] = None
        self.fd: Optional[int] = None
        self.idx: Dict[str, Any] = {"_version": 2, "_bundles": [], "_read_plan": []}
        # 若已有旧索引，合并（可选）
        if os.path.exists(INDEX):
            try:
                old = _load_index()
                # 简单合并 _bundles（不去重）
                if isinstance(old, dict) and "_bundles" in old:
                    self.idx["_bundles"].extend(old["_bundles"])
                    self.idx["_read_plan"].extend(old.get("_read_plan", []))
            except Exception:
                pass
        self.tail = os.path.getsize(STORE) if os.path.exists(STORE) else 0
        self.tail = _align(self.tail, ALIGN_FILE)

    def __enter__(self):
        _ensure_parent(LOCK)
        self.lock_fd = os.open(LOCK, os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(self.lock_fd, fcntl.LOCK_EX)

        _ensure_parent(STORE)
        self.fd = os.open(STORE, os.O_RDWR | os.O_CREAT)
        return self

    def __exit__(self, exc_type, exc, tb):
        try:
            if self.fd is not None:
                os.close(self.fd)
        finally:
            if self.lock_fd is not None:
                fcntl.flock(self.lock_fd, fcntl.LOCK_UN)
                os.close(self.lock_fd)
        if exc is None:
            _save_index(self.idx)

    def _ensure_size(self, size: int):
        cur = os.path.getsize(STORE) if os.path.exists(STORE) else 0
        if cur < size:
            with open(STORE, "r+b" if os.path.exists(STORE) else "w+b") as f:
                f.truncate(size)

    def put_bundle(self, bundle_id: str, items: List[Tuple[str, torch.Tensor]]):
        # 计算成员偏移、准备 memoryview（与你现有逻辑相同）
        rel = 0
        members_meta = []
        np_views: List[Tuple[memoryview, int, int]] = []  # (mv, rel_offset, nbytes)

        for name, t in items:
            tt = t.detach().contiguous()
            if tt.device.type != "cpu":
                tt = tt.cpu()
            arr, storage_dtype = _storage_view_numpy(tt)
            nbytes = arr.size * arr.itemsize

            rel = _align(rel, ALIGN_MEMBER)
            mv = memoryview(arr).cast("B")
            np_views.append((mv, rel, nbytes))

            members_meta.append({
                "name": name,
                "rel_offset": rel,
                "shape": tuple(tt.shape),
                "dtype": str(tt.dtype),
                "storage_dtype": storage_dtype,
            })
            rel += nbytes

        bundle_off  = _align(self.tail, ALIGN_BUNDLE)
        bundle_size = rel
        self.tail   = bundle_off + bundle_size
        self._ensure_size(self.tail)

        # 关 键：大块分片 pwrite，避免 2GiB 限制与 short write
        for mv, r, nbytes in np_views:
            _pwrite_all(self.fd, mv, bundle_off + r)

        # 元数据记录保持不变
        self.idx["_bundles"].append({
            "bundle": bundle_id,
            "offset": bundle_off,
            "nbytes": bundle_size,
            "members": members_meta,
        })
        self.idx["_read_plan"].append(bundle_id)

# -------------------- 模型加载（默认 Transformers / CPU） --------------------
def _load_model_hf(model_path: str, dtype_hint: Optional[torch.dtype]) -> torch.nn.Module:
    from transformers import AutoModelForCausalLM
    kwargs = {"device_map": "cpu"}
    if dtype_hint is not None:
        kwargs["torch_dtype"] = dtype_hint
    model = AutoModelForCausalLM.from_pretrained(model_path, **kwargs)
    model.eval()
    return model

def _load_model_vllm(model_path: str, dtype_hint: Optional[torch.dtype]) -> torch.nn.Module:
    from vllm import LLM
    llm = LLM(model=model_path, enforce_eager=True)
    model = llm.llm_engine.model_executor.driver_worker.model_runner.model
    model.eval()
    model.to("cpu")
    # vLLM 中不直接应用 dtype_hint，这里导出侧统一 _to_target_dtype 处理
    return model

def _target_torch_dtype() -> Optional[torch.dtype]:
    if TARGET_EXPORT_DTYPE == "keep":
        return None
    return getattr(torch, TARGET_EXPORT_DTYPE)

# -------------------- 主导出流程 --------------------
def export_model_to_memmap_bundled(model_path: str):
    t0 = time.time()
    print(f"[export] Loading model: {model_path} (loader={LOAD_WITH}) ...")
    target_dt = _target_torch_dtype()

    if LOAD_WITH == "vllm":
        model = _load_model_vllm(model_path, target_dt)
    else:
        model = _load_model_hf(model_path, target_dt)

    # 收集待导出条目
    all_items: List[Tuple[str, torch.Tensor, bool]] = []  # (name, tensor, is_buffer)
    for n, p in model.named_parameters():
        if not _should_skip(n, False):
            all_items.append((n, p, False))
    if INCLUDE_BUFFERS:
        for n, b in model.named_buffers():
            if not _should_skip(n, True):
                all_items.append((n, b, True))

    # 分组：先按 group_key，再按 dtype_key（保证 bundle 内 dtype 统一）
    grouped: Dict[str, List[Tuple[str, torch.Tensor]]] = {}
    for name, ten, _is_buf in all_items:
        g = _group_key(name)
        # 先转成目标导出 dtype（避免重复转换）
        ten = _to_target_dtype(ten)
        dk = _dtype_key(ten)   # keep 时为各自 dtype 名；否则统一为目标 dtype 名
        key = f"{g}::{dk}"
        grouped.setdefault(key, []).append((name, ten))

    # 组内排序（可选：按大小降序）
    if ORDER_IN_GROUP == "size":
        for k in grouped:
            grouped[k].sort(key=lambda it: it[1].numel() * it[1].element_size(), reverse=True)

    # 预分配
    if PREALLOC_SIZE_GB > 0:
        prealloc_bytes = int(PREALLOC_SIZE_GB * 1024**3)
    else:
        prealloc_bytes = _estimate_bytes_bundled(grouped)
    _preallocate_store(prealloc_bytes)

    # 导出
    n_params = n_bufs = 0
    bytes_total = 0

    with ExportSession() as sess, torch.no_grad():
        # 固定写入顺序：按组名排序，增强导入端顺序性
        for bundle_id in sorted(grouped.keys()):
            items = grouped[bundle_id]
            # 统计（粗略）：成员总字节
            bundle_bytes = 0
            for _, t in items:
                arr, _sd = _storage_view_numpy(t)
                bundle_bytes += arr.size * arr.itemsize

            sess.put_bundle(bundle_id, items)
            bytes_total += bundle_bytes

            # 计数展示（参数/缓冲区粗略统计，不区分这里）
            n_params += len(items)

    # 预热（可选）
    print("[export] Prewarming store...")
    touched = prewarm_store()

    # 结束
    dt = time.time() - t0
    gib = bytes_total / (1024**3)
    print(f"\n[export-bundled] Summary")
    print(f"  - Bundles: {len(grouped)}")
    print(f"  - Items  : {n_params} tensors")
    print(f"  - Target dtype: {TARGET_EXPORT_DTYPE}")
    print(f"  - Group strategy: {GROUP_STRATEGY} / order: {ORDER_IN_GROUP}")
    print(f"  - Size   : {gib:.2f} GiB")
    print(f"  - Time   : {dt:.1f}s  (~{gib/max(dt,1e-6):.2f} GiB/s)")
    print(f"  - Store  : {STORE}")
    print(f"  - Index  : {INDEX}")
    print(f"  - Prewarm: {touched/1e9:.2f} GB")

# -------------------- 入口 --------------------
if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python export_weights_memmap_bundled.py <model_or_path>")
        sys.exit(2)
    model_path = sys.argv[1]
    export_model_to_memmap_bundled(model_path)