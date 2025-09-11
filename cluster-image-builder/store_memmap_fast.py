import os, mmap, pickle, fcntl
import numpy as np
import torch
from typing import Dict, Any, Optional

STORE = os.environ.get("MM_STORE_PATH", "/dev/shm/weights.store")
INDEX = os.environ.get("MM_INDEX_PATH", "/dev/shm/weights.index.pkl")
LOCK  = os.environ.get("MM_LOCK_PATH",  "/dev/shm/weights.lock")

_mmap: Optional[mmap.mmap] = None
_mmap_fd: Optional[int] = None

def _load_index() -> Dict[str, Any]:
    if not os.path.exists(INDEX):
        return {}
    with open(INDEX, "rb") as f:
        return pickle.load(f)

def _get_mmap() -> mmap.mmap:
    global _mmap, _mmap_fd
    if _mmap is None:
        _mmap_fd = os.open(STORE, os.O_RDWR)
        _mmap = mmap.mmap(_mmap_fd, 0, access=mmap.ACCESS_WRITE)  # 可写共享
        # 关键优化：给内核个暗示，尽量预取到页缓存里
        try:
            _mmap.madvise(mmap.MADV_WILLNEED)      # 未来将会使用
            _mmap.madvise(mmap.MADV_SEQUENTIAL)    # 近似顺序访问
        except (AttributeError, ValueError):
            pass
    return _mmap

def get_tensor_zero_copy(name: str) -> torch.Tensor:
    idx = _load_index()
    info = idx[name]
    offset = int(info["offset"])
    nbytes = int(info["nbytes"])
    shape  = tuple(info["shape"])
    storage_dtype = info["storage_dtype"]     # e.g. "uint16" for bf16-packed
    logical_dtype = info["dtype"].replace("torch.", "")

    mm = _get_mmap()

    # 直接从 mmap “解释”为目标 dtype（不做任何 bytes 切片！）
    if storage_dtype == "uint16" and "bfloat16" in info["dtype"]:
        t = torch.frombuffer(mm, dtype=torch.bfloat16,
                             count=nbytes // 2, offset=offset).reshape(shape)
    else:
        storage_dt = getattr(torch, storage_dtype)  # 如 torch.float16/float32/...
        itemsize = torch.tensor([], dtype=storage_dt).element_size()
        t = torch.frombuffer(mm, dtype=storage_dt,
                             count=nbytes // itemsize, offset=offset).reshape(shape)

        # 位宽不同才允许转换（会拷贝）；同位宽请直接用 frombuffer 的 dtype 达成 0-copy “重解释”
        if logical_dtype and getattr(torch, logical_dtype, None) not in (None, t.dtype):
            target_dt = getattr(torch, logical_dtype)
            if torch.tensor([], dtype=target_dt).element_size() != itemsize:
                t = t.to(target_dt)  # 只有这会拷贝（位宽不同无法避免）

    return t

def cleanup():
    global _mmap, _mmap_fd
    if _mmap is not None:
        _mmap.close()
        _mmap = None
    if _mmap_fd is not None:
        os.close(_mmap_fd)
        _mmap_fd = None