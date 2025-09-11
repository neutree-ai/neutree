from typing import Optional
from vllm.device_allocator.cumem import CuMemAllocator, create_and_map, find_loaded_library
import os
import ctypes

print("---> load raw libcudart <---")
_libcudart_path = find_loaded_library("libcudart") or "libcudart.so"
_rt = ctypes.CDLL(_libcudart_path)

# 绑定需要的函数
cudaStream_t = ctypes.c_void_p
_rt.cudaStreamCreate.argtypes = [ctypes.POINTER(cudaStream_t)]
_rt.cudaStreamCreate.restype  = ctypes.c_int
_rt.cudaStreamDestroy.argtypes= [cudaStream_t]
_rt.cudaStreamDestroy.restype = ctypes.c_int
_rt.cudaStreamSynchronize.argtypes = [cudaStream_t]
_rt.cudaStreamSynchronize.restype  = ctypes.c_int
_rt.cudaMemcpyAsync.argtypes = [ctypes.c_void_p, ctypes.c_void_p,
                                ctypes.c_size_t, ctypes.c_int, cudaStream_t]
_rt.cudaMemcpyAsync.restype  = ctypes.c_int
_cudaMemcpyDefault = 4

print("---> patch_cumem <---")
os.environ["VLLM_ENABLE_V1_MULTIPROCESSING"] = "0"

# 先备份原始方法，方便还原
_original_wake_up = CuMemAllocator.wake_up

def patched_wake_up(self, tags: Optional[list[str]] = None) -> None:
    print("patched version, stream")
    NUM_STREAMS = 6
    streams = []
    for _ in range(NUM_STREAMS):
        sp = cudaStream_t()
        err = _rt.cudaStreamCreate(ctypes.byref(sp))
        if err != 0:
            raise RuntimeError(f"cudaStreamCreate failed: {err}")
        streams.append(sp)
    si = 0
    

    # ---- 统计用容器（不改变原有逻辑）----
    sizes = []                    # 每个需要恢复的 buffer 大小（bytes）
    per_tag_cnt = {}              # tag -> count
    per_tag_bytes = {}            # tag -> bytes
    total_copy_bytes = 0

    def _rec(tag, n):
        sizes.append(n)
        per_tag_cnt[tag] = per_tag_cnt.get(tag, 0) + 1
        per_tag_bytes[tag] = per_tag_bytes.get(tag, 0) + n
        nonlocal total_copy_bytes
        total_copy_bytes += n
    
    for ptr, data in self.pointer_to_data.items():
        if tags is not None and data.tag not in tags:
            continue
        handle = data.handle
        create_and_map(handle)
        cpu_backup_tensor = data.cpu_backup_tensor
        if cpu_backup_tensor is None:
            continue
        nbytes = cpu_backup_tensor.numel() * cpu_backup_tensor.element_size()
        src_ptr = cpu_backup_tensor.data_ptr()
        dst_ptr = ptr
        s = streams[si % NUM_STREAMS]
        si += 1
        _rec(data.tag, nbytes)
        err = _rt.cudaMemcpyAsync(ctypes.c_void_p(dst_ptr),
                                  ctypes.c_void_p(src_ptr),
                                  ctypes.c_size_t(nbytes),
                                  ctypes.c_int(_cudaMemcpyDefault),
                                  s)
        if err != 0:
            raise RuntimeError(f"cudaMemcpyAsync failed: {err}")
        data.cpu_backup_tensor = None

    for s in streams:
        _rt.cudaStreamSynchronize(s)
        _rt.cudaStreamDestroy(s)
        
            # ---- 打印分布（聚合信息，避免刷屏）----
    if sizes:
        sizes_sorted = sorted(sizes)
        def fmt_b(x):  # 简单人类可读
            gb = 1024**3; mb = 1024**2; kb = 1024
            return (f"{x/gb:.2f} GiB" if x >= gb else
                    f"{x/mb:.2f} MiB" if x >= mb else
                    f"{x/kb:.2f} KiB")
        def pct(p):
            idx = min(len(sizes_sorted)-1, max(0, int(len(sizes_sorted)*p)))
            return sizes_sorted[idx]
        # 分桶（按经验阈值）
        buckets = [
                (1*1024**2, "<1MiB"),
            (8*1024**2, "1–8MiB"),
            (64*1024**2, "8–64MiB"),
            (256*1024**2, "64–256MiB"),
            (1024**3, "256MiB–1GiB"),
            (float("inf"), ">=1GiB"),
        ]
        hist = {name: 0 for _, name in buckets}
        for n in sizes:
            for lim, name in buckets:
                if n <= lim:
                    hist[name] += 1
                    break
        print("[wake_up stats] count=", len(sizes),
              " total=", fmt_b(total_copy_bytes),
              " min=", fmt_b(sizes_sorted[0]),
              " p50=", fmt_b(pct(0.50)),
              " p90=", fmt_b(pct(0.90)),
              " p99=", fmt_b(pct(0.99)),
              " max=", fmt_b(sizes_sorted[-1]))
        print("[wake_up stats] histogram=", hist)
        print("[wake_up stats] per_tag_cnt=", {k: per_tag_cnt[k] for k in sorted(per_tag_cnt)})
        print("[wake_up stats] per_tag_bytes=", {k: fmt_b(per_tag_bytes[k]) for k in sorted(per_tag_bytes)})

CuMemAllocator.wake_up = patched_wake_up