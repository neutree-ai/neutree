# export_weights_memmap_optimized.py
import os, time, re, gc
import torch
import numpy as np
from vllm import LLM
from store_memmap import put_tensor, prewarm_store, STORE, ALIGN
from typing import List, Tuple

os.environ["VLLM_ENABLE_V1_MULTIPROCESSING"] = "0"

# ---- 导出策略 ----
TARGET_EXPORT_DTYPE = os.environ.get("EXPORT_DTYPE", "keep")
INCLUDE_BUFFERS = os.environ.get("EXPORT_INCLUDE_BUFFERS", "1") == "1"
_default_skips = [r"cos_sin_cache", r".*rotary.*cache.*"]
SKIP_PATTERNS = os.environ.get("EXPORT_SKIP_PATTERNS", ",".join(_default_skips)).split(",")
ORDER_BY_SIZE = os.environ.get("EXPORT_ORDER", "size") == "size"  # 默认改为 size
OPTIMIZE_LAYOUT = os.environ.get("EXPORT_OPTIMIZE_LAYOUT", "1") == "1"  # 内存布局优化
PREALLOC_SIZE_GB = float(os.environ.get("EXPORT_PREALLOC_GB", "0"))  # 预分配大小

def _to_target_dtype(t: torch.Tensor) -> torch.Tensor:
    if TARGET_EXPORT_DTYPE == "keep":
        return t
    dt = getattr(torch, TARGET_EXPORT_DTYPE)
    return t.to(dtype=dt)

def _should_skip(name: str, is_buffer: bool) -> bool:
    for pat in SKIP_PATTERNS:
        if pat and re.search(pat, name):
            return True
    return False

def _estimate_storage_size(model) -> int:
    """估算需要的存储空间"""
    total_bytes = 0
    
    for name, p in model.named_parameters():
        if not _should_skip(name, False):
            dtype = getattr(torch, TARGET_EXPORT_DTYPE) if TARGET_EXPORT_DTYPE != "keep" else p.dtype
            elem_size = 2 if dtype == torch.bfloat16 else torch.tensor([], dtype=dtype).element_size()
            total_bytes += p.numel() * elem_size
    
    if INCLUDE_BUFFERS:
        for name, b in model.named_buffers():
            if not _should_skip(name, True):
                dtype = getattr(torch, TARGET_EXPORT_DTYPE) if TARGET_EXPORT_DTYPE != "keep" else b.dtype
                elem_size = 2 if dtype == torch.bfloat16 else torch.tensor([], dtype=dtype).element_size()
                total_bytes += b.numel() * elem_size
    
    # 加上对齐开销（估算 10%）
    return int(total_bytes * 1.1)

def _optimize_parameter_order(params: List[Tuple[str, torch.nn.Parameter]]) -> List[Tuple[str, torch.nn.Parameter]]:
    """优化参数顺序以提高内存局部性"""
    
    if not OPTIMIZE_LAYOUT:
        if ORDER_BY_SIZE:
            # 大块优先
            return sorted(params, key=lambda x: x[1].numel() * x[1].element_size(), reverse=True)
        return params
    
    # 高级优化：按层分组 + 大小排序
    # 目标：同一层的权重放在一起，提高缓存命中率
    
    layer_groups = {}
    other_params = []
    
    for name, param in params:
        # 尝试提取层号
        import re
        match = re.search(r'\.(\d+)\.', name)
        if match:
            layer_num = int(match.group(1))
            if layer_num not in layer_groups:
                layer_groups[layer_num] = []
            layer_groups[layer_num].append((name, param))
        else:
            other_params.append((name, param))
    
    # 重新组织：层内按大小排序，层间按层号排序
    result = []
    
    # 先加入非层参数（通常是 embeddings 等）
    other_params.sort(key=lambda x: x[1].numel() * x[1].element_size(), reverse=True)
    result.extend(other_params)
    
    # 按层号顺序加入各层参数
    for layer_num in sorted(layer_groups.keys()):
        layer_params = layer_groups[layer_num]
        # 层内按大小排序
        layer_params.sort(key=lambda x: x[1].numel() * x[1].element_size(), reverse=True)
        result.extend(layer_params)
    
    return result

def _preallocate_store(size_bytes: int):
    """预分配存储文件，减少文件系统开销"""
    if os.path.exists(STORE):
        current_size = os.path.getsize(STORE)
        if current_size >= size_bytes:
            return
    
    print(f"[export] Preallocating {size_bytes / 1e9:.2f} GB...")
    
    # 使用 fallocate 快速分配（Linux）
    try:
        import subprocess
        subprocess.run(
            ["fallocate", "-l", str(size_bytes), STORE],
            check=True,
            capture_output=True
        )
        print(f"[export] Fast preallocated using fallocate")
    except:
        # 降级到 truncate
        os.makedirs(os.path.dirname(STORE) or ".", exist_ok=True)
        with open(STORE, "wb") as f:
            f.truncate(size_bytes)
        print(f"[export] Preallocated using truncate")

def export_model_to_memmap_optimized(model_path: str):
    """优化版导出：内存布局优化 + 批量写入"""
    
    t0 = time.time()
    
    # 加载模型
    print(f"[export] Loading model {model_path}...")
    llm = LLM(model=model_path, enforce_eager=True)
    model = llm.llm_engine.model_executor.driver_worker.model_runner.model
    model.eval()
    
    # 估算并预分配空间
    if PREALLOC_SIZE_GB > 0:
        prealloc_bytes = int(PREALLOC_SIZE_GB * 1024**3)
    else:
        prealloc_bytes = _estimate_storage_size(model)
    
    _preallocate_store(prealloc_bytes)
    
    # 收集所有参数
    all_params = []
    for name, p in model.named_parameters():
        if not _should_skip(name, False):
            all_params.append((name, p))
    
    all_buffers = []
    if INCLUDE_BUFFERS:
        for name, b in model.named_buffers():
            if not _should_skip(name, True):
                all_buffers.append((name, b))
    
    # 优化顺序
    all_params = _optimize_parameter_order(all_params)
    all_buffers = _optimize_parameter_order(all_buffers)
    
    n_params = n_bufs = 0
    bytes_params = bytes_bufs = 0
    
    # 批量写入参数
    print(f"[export] Writing {len(all_params)} parameters...")
    
    with torch.no_grad():
        # 使用进度显示
        for i, (name, p) in enumerate(all_params):
            pt = _to_target_dtype(p)
            put_tensor(name, pt)
            n_params += 1
            bytes_params += pt.numel() * (2 if pt.dtype == torch.bfloat16 else pt.element_size())
            
            # 定期显示进度
            if (i + 1) % 100 == 0:
                print(f"[export] Processed {i+1}/{len(all_params)} parameters...")
        
        # 批量写入 buffers
        if all_buffers:
            print(f"[export] Writing {len(all_buffers)} buffers...")
            
            for i, (name, b) in enumerate(all_buffers):
                bt = _to_target_dtype(b)
                put_tensor(name, bt)
                n_bufs += 1
                bytes_bufs += bt.numel() * (2 if bt.dtype == torch.bfloat16 else bt.element_size())
                
                if (i + 1) % 100 == 0:
                    print(f"[export] Processed {i+1}/{len(all_buffers)} buffers...")
    
    # 清理模型内存
    del llm
    del model
    gc.collect()
    torch.cuda.empty_cache()
    
    # 预热文件缓存
    print("[export] Prewarming store...")
    touched = prewarm_store()
    
    dt = time.time() - t0
    total_gib = (bytes_params + bytes_bufs) / (1024**3)
    
    print(f"\n[export-optimized] Summary:")
    print(f"  - Parameters: {n_params} ({bytes_params/1e9:.2f} GB)")
    print(f"  - Buffers: {n_bufs} ({bytes_bufs/1e9:.2f} GB)")
    print(f"  - Target dtype: {TARGET_EXPORT_DTYPE}")
    print(f"  - Layout optimization: {OPTIMIZE_LAYOUT}")
    print(f"  - Total size: {total_gib:.2f} GiB")
    print(f"  - Export time: {dt:.1f}s ({total_gib/dt:.2f} GiB/s)")
    print(f"  - Store prewarmed: {touched/1e9:.2f} GB")
    print(f"  - Store path: {STORE}")

if __name__ == "__main__":
    # 使用示例：
    # EXPORT_DTYPE=bfloat16 EXPORT_OPTIMIZE_LAYOUT=1 EXPORT_PREALLOC_GB=10 python export_weights_memmap_optimized.py
    
    import sys
    model_path = sys.argv[1] if len(sys.argv) > 1 else "Qwen/Qwen3-14B"
    export_model_to_memmap_optimized(model_path)