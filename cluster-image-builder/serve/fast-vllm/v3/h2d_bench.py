#!/usr/bin/env python3
import argparse, time, os, ctypes, subprocess, re
from ctypes import c_void_p, c_size_t, c_int, byref

def run_cmd(cmd):
    """安全执行命令并返回输出"""
    try:
        return subprocess.check_output(cmd, shell=True, text=True).strip()
    except:
        return "N/A"

def collect_system_info():
    """收集系统配置信息"""
    print("=== 系统信息收集 ===")
    
    # CPU 信息
    cpu_model = run_cmd("lscpu | grep 'Model name' | head -1")
    numa_nodes = run_cmd("lscpu | grep 'NUMA node(s)'")
    print(f"CPU: {cpu_model}")
    print(f"NUMA: {numa_nodes}")
    
    # 内存信息
    mem_info = run_cmd("free -h | grep '^Mem'")
    print(f"Memory: {mem_info}")
    
    # GPU 拓扑信息
    print("\n=== GPU 拓扑信息 ===")
    gpu_topo = run_cmd("nvidia-smi topo -m")
    print(gpu_topo)
    
    # PCIe 信息
    print("\n=== PCIe 配置 ===")
    pcie_info = run_cmd("lspci | grep -i nvidia")
    print(f"GPU PCIe 设备: {pcie_info}")
    
    # 获取详细的 PCIe 配置
    try:
        nvidia_devices = run_cmd("lspci | grep -i nvidia | cut -d' ' -f1")
        for device in nvidia_devices.split('\n'):
            if device:
                pcie_details = run_cmd(f"lspci -vvv -s {device} | grep -E '(LnkCap|LnkSta)'")
                print(f"设备 {device} PCIe 详情:")
                print(pcie_details)
    except:
        pass
    
    # NUMA 绑定状态
    print("\n=== NUMA 绑定状态 ===")
    current_numa = run_cmd("numastat -p $$")
    print(current_numa)
    
    # GPU 内存和时钟信息
    print("\n=== GPU 状态 ===")
    gpu_status = run_cmd("nvidia-smi -q -d CLOCK,MEMORY,POWER")
    print(gpu_status)
    
    # 驱动版本
    driver_version = run_cmd("nvidia-smi --query-gpu=driver_version --format=csv,noheader")
    cuda_version = run_cmd("nvcc --version | grep 'release' || echo 'nvcc not found'")
    print(f"\n驱动版本: {driver_version}")
    print(f"CUDA 版本: {cuda_version}")

# ---- 原有的 CUDA 绑定代码 ----
def load_libcudart():
    path = None
    try:
        with open("/proc/self/maps") as f:
            for line in f:
                if "libcudart" in line and ".so" in line:
                    p = line[line.index("/"):].strip()
                    if "libcudart" in os.path.basename(p):
                        path = p
                        break
    except Exception:
        pass
    return ctypes.CDLL(path or "libcudart.so")

rt = load_libcudart()

cudaMemcpyHostToDevice = 1
cudaMemcpyDeviceToHost = 2
cudaMemcpyDefault = 4
cudaHostAllocDefault = 0
cudaHostAllocPortable = 1
cudaHostAllocMapped = 2
cudaHostAllocWriteCombined = 4

cudaStream_t = c_void_p

# 函数签名
rt.cudaSetDevice.argtypes = [c_int]
rt.cudaSetDevice.restype  = c_int
rt.cudaDeviceSynchronize.argtypes = []
rt.cudaDeviceSynchronize.restype  = c_int

rt.cudaMalloc.argtypes = [ctypes.POINTER(c_void_p), c_size_t]
rt.cudaMalloc.restype  = c_int
rt.cudaFree.argtypes   = [c_void_p]
rt.cudaFree.restype    = c_int

rt.cudaHostAlloc.argtypes = [ctypes.POINTER(c_void_p), c_size_t, c_int]
rt.cudaHostAlloc.restype  = c_int
rt.cudaFreeHost.argtypes  = [c_void_p]
rt.cudaFreeHost.restype   = c_int

rt.cudaMemcpy.argtypes = [c_void_p, c_void_p, c_size_t, c_int]
rt.cudaMemcpy.restype  = c_int
rt.cudaMemcpyAsync.argtypes = [c_void_p, c_void_p, c_size_t, c_int, cudaStream_t]
rt.cudaMemcpyAsync.restype  = c_int

rt.cudaStreamCreate.argtypes  = [ctypes.POINTER(cudaStream_t)]
rt.cudaStreamCreate.restype   = c_int
rt.cudaStreamDestroy.argtypes = [cudaStream_t]
rt.cudaStreamDestroy.restype  = c_int
rt.cudaStreamSynchronize.argtypes = [cudaStream_t]
rt.cudaStreamSynchronize.restype  = c_int

# 添加性能相关的API
rt.cudaDeviceSetCacheConfig.argtypes = [c_int]
rt.cudaDeviceSetCacheConfig.restype = c_int

def check(err, msg):
    if err != 0:
        raise RuntimeError(f"{msg} failed with code {err}")

def human(nbytes):
    gb=1024**3; mb=1024**2
    if nbytes >= gb: return f"{nbytes/gb:.2f} GiB"
    if nbytes >= mb: return f"{nbytes/mb:.2f} MiB"
    return f"{nbytes/1024:.2f} KiB"

def gbps(nbytes, secs):
    return (nbytes / (1024**3)) / max(secs, 1e-9)

def enhanced_bench(total_bytes, chunk_bytes, streams, device=0, iters=3):
    """增强的性能测试，包含多种分配方式和优化"""
    
    print(f"\n=== 开始性能测试 (设备 {device}) ===")
    check(rt.cudaSetDevice(device), "cudaSetDevice")
    
    # 设置GPU为性能模式
    try:
        rt.cudaDeviceSetCacheConfig(1)  # cudaFuncCachePreferL1
    except:
        pass
    
    # 分配设备内存
    dptr = c_void_p()
    check(rt.cudaMalloc(byref(dptr), c_size_t(total_bytes)), "cudaMalloc")
    
    print(f"\n--- 测试不同的主机内存分配方式 ---")
    
    # 测试1: 标准固定内存
    hptr1 = c_void_p()
    check(rt.cudaHostAlloc(byref(hptr1), c_size_t(total_bytes), cudaHostAllocDefault), "cudaHostAlloc Default")
    
    # 预热
    check(rt.cudaMemcpy(dptr, hptr1, c_size_t(min(total_bytes, 8*1024*1024)), cudaMemcpyHostToDevice), "warmup")
    check(rt.cudaDeviceSynchronize(), "device sync")
    
    # 同步传输测试
    times = []
    for i in range(iters):
        t0 = time.perf_counter()
        check(rt.cudaMemcpy(dptr, hptr1, c_size_t(total_bytes), cudaMemcpyHostToDevice), "sync memcpy")
        check(rt.cudaDeviceSynchronize(), "device sync")
        t1 = time.perf_counter()
        times.append(t1 - t0)
    
    avg_time = sum(times) / len(times)
    min_time = min(times)
    max_time = max(times)
    print(f"同步传输 (标准固定内存): 平均={gbps(total_bytes, avg_time):.2f} GB/s, "
          f"最佳={gbps(total_bytes, min_time):.2f} GB/s, "
          f"最差={gbps(total_bytes, max_time):.2f} GB/s")
    
    # 测试2: Write-Combined 内存
    hptr2 = c_void_p()
    try:
        check(rt.cudaHostAlloc(byref(hptr2), c_size_t(total_bytes), 
                              cudaHostAllocDefault | cudaHostAllocWriteCombined), "cudaHostAlloc WC")
        
        times = []
        for i in range(iters):
            t0 = time.perf_counter()
            check(rt.cudaMemcpy(dptr, hptr2, c_size_t(total_bytes), cudaMemcpyHostToDevice), "sync memcpy WC")
            check(rt.cudaDeviceSynchronize(), "device sync")
            t1 = time.perf_counter()
            times.append(t1 - t0)
        
        avg_time = sum(times) / len(times)
        min_time = min(times)
        print(f"同步传输 (Write-Combined): 平均={gbps(total_bytes, avg_time):.2f} GB/s, "
              f"最佳={gbps(total_bytes, min_time):.2f} GB/s")
        
        check(rt.cudaFreeHost(hptr2), "cudaFreeHost WC")
    except Exception as e:
        print(f"Write-Combined 内存测试失败: {e}")
    
    # 测试3: 多流异步传输 (多种chunk大小)
    print(f"\n--- 异步传输测试 (流数量: {streams}) ---")
    
    chunk_sizes = [chunk_bytes // 4, chunk_bytes // 2, chunk_bytes, chunk_bytes * 2, chunk_bytes * 4]
    
    for chunk_size in chunk_sizes:
        if chunk_size > total_bytes:
            continue
            
        # 创建流
        stream_arr = []
        for _ in range(streams):
            sp = cudaStream_t()
            check(rt.cudaStreamCreate(byref(sp)), "cudaStreamCreate")
            stream_arr.append(sp)
        
        times = []
        for iter_i in range(iters):
            t0 = time.perf_counter()
            
            off = 0
            si = 0
            while off < total_bytes:
                cur = min(chunk_size, total_bytes - off)
                sp = stream_arr[si % streams]
                check(rt.cudaMemcpyAsync(
                    c_void_p(dptr.value + off),
                    c_void_p(hptr1.value + off),
                    c_size_t(cur),
                    cudaMemcpyHostToDevice,
                    sp
                ), "cudaMemcpyAsync")
                si += 1
                off += cur
            
            # 等待所有流
            for sp in stream_arr:
                check(rt.cudaStreamSynchronize(sp), "cudaStreamSynchronize")
            
            t1 = time.perf_counter()
            times.append(t1 - t0)
        
        # 清理流
        for sp in stream_arr:
            rt.cudaStreamDestroy(sp)
        
        avg_time = sum(times) / len(times)
        min_time = min(times)
        chunks_per_transfer = (total_bytes + chunk_size - 1) // chunk_size
        print(f"异步传输 chunk={human(chunk_size)} ({chunks_per_transfer}块): "
              f"平均={gbps(total_bytes, avg_time):.2f} GB/s, "
              f"最佳={gbps(total_bytes, min_time):.2f} GB/s")
    
    # 清理
    check(rt.cudaFree(dptr), "cudaFree")
    check(rt.cudaFreeHost(hptr1), "cudaFreeHost")

def main():
    p = argparse.ArgumentParser(description="增强的 CUDA 内存传输性能诊断工具")
    p.add_argument("--device", type=int, default=0)
    p.add_argument("--total-gb", type=float, default=4.0, help="总传输大小 (GiB)")
    p.add_argument("--chunk-mib", type=int, default=64, help="基础分块大小 (MiB)")
    p.add_argument("--streams", type=int, default=6, help="CUDA 流数量")
    p.add_argument("--iters", type=int, default=3, help="测试迭代次数")
    p.add_argument("--skip-sysinfo", action="store_true", help="跳过系统信息收集")
    
    args = p.parse_args()
    
    if not args.skip_sysinfo:
        collect_system_info()
    
    total_bytes = int(args.total_gb * (1024**3))
    chunk_bytes = int(args.chunk_mib * (1024**2))
    
    print(f"\n=== 测试参数 ===")
    print(f"设备: {args.device}")
    print(f"总大小: {human(total_bytes)}")
    print(f"基础分块: {human(chunk_bytes)}")
    print(f"流数量: {args.streams}")
    print(f"迭代次数: {args.iters}")
    
    enhanced_bench(total_bytes, chunk_bytes, args.streams, device=args.device, iters=args.iters)
    
    print(f"\n=== 优化建议 ===")
    print("1. 确保使用 numactl 绑定到 GPU 对应的 NUMA 节点")
    print("2. 检查 PCIe 链路是否为 x16 @ PCIe 4.0")
    print("3. 验证驱动版本兼容性（避免 470.57.02）")
    print("4. 考虑使用更大的传输块大小")
    print("5. 在 AWS 上考虑迁移到 P5 实例")

if __name__ == "__main__":
    main()