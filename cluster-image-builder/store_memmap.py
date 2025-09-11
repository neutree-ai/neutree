# store_memmap_optimized.py
import os, pickle, fcntl, mmap
import numpy as np
import torch
from typing import Tuple, Dict, Any, Optional
from concurrent.futures import ThreadPoolExecutor

# ---- 配置路径 ----
STORE = os.environ.get("MM_STORE_PATH", "/dev/shm/weights.store")
INDEX = os.environ.get("MM_INDEX_PATH", "/dev/shm/weights.index.pkl")
LOCK  = os.environ.get("MM_LOCK_PATH",  "/dev/shm/weights.lock")
ALIGN = int(os.environ.get("MM_ALIGN_BYTES", "4096"))

# ---- 性能优化配置 ----
USE_ZERO_COPY = os.environ.get("MM_ZERO_COPY", "1") == "1"
PREFETCH_SIZE = int(os.environ.get("MM_PREFETCH_MB", "256")) * 1024 * 1024  # 预取大小

# 保持原有的辅助函数不变
def _ensure_parent(p: str):
    d = os.path.dirname(p) or "."
    os.makedirs(d, exist_ok=True)

def _align(x: int, k: int = ALIGN) -> int:
    return (x + (k - 1)) // k * k

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

def _lock():
    _ensure_parent(LOCK)
    fd = os.open(LOCK, os.O_CREAT | os.O_RDWR, 0o600)
    fcntl.flock(fd, fcntl.LOCK_EX)
    return fd

def _unlock(fd):
    fcntl.flock(fd, fcntl.LOCK_UN)
    os.close(fd)

def _ensure_size(path: str, size: int) -> None:
    mode = "r+b" if os.path.exists(path) else "w+b"
    with open(path, mode) as f:
        f.seek(0, os.SEEK_END)
        if f.tell() < size:
            f.truncate(size)

# put_tensor 保持不变
def put_tensor(name: str, t: torch.Tensor) -> None:
    """把张量写入 memmap 仓库；顺序/覆盖写并进行对齐；bf16 以 uint16 位存。"""
    t = t.detach().contiguous().cpu()
    if t.dtype == torch.bfloat16:
        np_src = t.view(torch.uint16).numpy()
        storage_dtype = "uint16"
    else:
        np_src = t.numpy()
        storage_dtype = str(np_src.dtype)

    raw = np_src.tobytes(order="C")
    nbytes = len(raw)

    fd = _lock()
    try:
        idx = _load_index()
        if name in idx and int(idx[name]["nbytes"]) == nbytes:
            offset = int(idx[name]["offset"])
        else:
            tail = os.path.getsize(STORE) if os.path.exists(STORE) else 0
            offset = _align(tail, ALIGN)

        need = offset + nbytes
        _ensure_size(STORE, need)

        with open(STORE, "r+b") as f:
            f.seek(offset)
            f.write(raw)

        idx[name] = {
            "offset": offset,
            "nbytes": nbytes,
            "shape": tuple(t.shape),
            "dtype": str(t.dtype),
            "storage_dtype": storage_dtype,
        }
        _save_index(idx)
    finally:
        _unlock(fd)

# 全局 mmap 缓存，避免重复打开
_mmap_cache: Optional[mmap.mmap] = None
_mmap_fd: Optional[int] = None

def _get_mmap():
    """获取或创建全局 mmap 对象"""
    global _mmap_cache, _mmap_fd
    if _mmap_cache is None:
        _mmap_fd = os.open(STORE, os.O_RDWR)
        _mmap_cache = mmap.mmap(_mmap_fd, 0, access=mmap.ACCESS_WRITE)
    return _mmap_cache

def get_tensor_zero_copy(name: str) -> torch.Tensor:
    info = _load_index()[name]
    offset = int(info["offset"])
    nbytes = int(info["nbytes"])
    shape = tuple(info["shape"])
    storage_dtype = info["storage_dtype"]
    logical = info["dtype"].replace("torch.", "")

    mm = _get_mmap()

    # bfloat16 存储为 uint16 的特殊位解释
    if storage_dtype == "uint16" and "bfloat16" in info["dtype"]:
        t = torch.frombuffer(mm, dtype=torch.bfloat16, count=nbytes // 2, offset=offset)
        return t.reshape(shape)

    # 其他存储类型
    storage_dt = getattr(torch, storage_dtype)  # 例如 torch.float16 / torch.int32
    itemsize = torch.tensor([], dtype=storage_dt).element_size()
    t = torch.frombuffer(mm, dtype=storage_dt, count=nbytes // itemsize, offset=offset).reshape(shape)

    logical_dt = getattr(torch, logical, None)
    if logical_dt is not None and logical_dt != t.dtype:
        if torch.tensor([], dtype=logical_dt).element_size() == itemsize:
            # 同位宽：直接按逻辑 dtype 重解释（再次 frombuffer 最干净）
            t = torch.frombuffer(mm, dtype=logical_dt, count=nbytes // itemsize, offset=offset).reshape(shape)
        else:
            t = t.to(logical_dt)  # 会拷贝
    return t

def get_tensor(name: str) -> torch.Tensor:
    """兼容接口：根据配置选择零拷贝或普通版本"""
    if USE_ZERO_COPY:
        return get_tensor_zero_copy(name)
    
    # 原版实现（保留兼容性）
    idx = _load_index()
    if name not in idx:
        raise KeyError(f"{name} not found in memmap index: {INDEX}")

    info = idx[name]
    offset = int(info["offset"])
    shape = tuple(info["shape"])
    storage_dtype = np.dtype(info["storage_dtype"])

    mm = np.memmap(STORE, mode="r+", dtype=storage_dtype,  # 改为 r+ 避免某些拷贝
                   offset=offset, shape=shape)
    
    if info["storage_dtype"] == "uint16" and "bfloat16" in info["dtype"]:
        t = torch.from_numpy(np.asarray(mm, order="C")).view(torch.bfloat16)
    else:
        t = torch.from_numpy(np.asarray(mm, order="C"))
        logical = info["dtype"].replace("torch.", "")
        dt = getattr(torch, logical, None)
        if dt is not None and t.dtype != dt:
            t = t.to(dt)
    
    return t

def get_tensor_batch(names: list) -> Dict[str, torch.Tensor]:
    """批量获取多个 tensor，支持并行读取"""
    with ThreadPoolExecutor(max_workers=8) as executor:
        futures = {name: executor.submit(get_tensor, name) for name in names}
        return {name: future.result() for name, future in futures.items()}

def prewarm_store(read_bytes: int = 256 << 20) -> int:
    """增强版预热：使用 madvise 提示内核预取"""
    if not os.path.exists(STORE):
        return 0
    
    size = os.path.getsize(STORE)
    
    # 使用 madvise 提示内核预取
    try:
        import ctypes
        import ctypes.util
        
        libc = ctypes.CDLL(ctypes.util.find_library('c'))
        MADV_WILLNEED = 3  # Linux constant
        
        fd = os.open(STORE, os.O_RDONLY)
        mm = mmap.mmap(fd, size, access=mmap.ACCESS_READ)
        
        # 告诉内核我们即将访问这些页面
        libc.madvise(ctypes.c_void_p(mm.tell()), ctypes.c_size_t(size), MADV_WILLNEED)
        
        os.close(fd)
        return size
    except:
        # 降级到原版实现
        touched = 0
        with open(STORE, "rb") as f:
            while touched < size:
                chunk = f.read(min(read_bytes, size - touched))
                if not chunk:
                    break
                touched += len(chunk)
        return touched

def cleanup():
    """清理全局资源"""
    global _mmap_cache, _mmap_fd
    if _mmap_cache is not None:
        _mmap_cache.close()
        _mmap_cache = None
    if _mmap_fd is not None:
        os.close(_mmap_fd)
        _mmap_fd = None