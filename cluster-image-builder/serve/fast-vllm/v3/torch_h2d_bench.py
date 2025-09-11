#!/usr/bin/env python3
"""
真实模型权重 H2D 传输性能测试
测试预加载到CPU内存的真实模型权重传输到GPU的性能
"""

import torch
import time
import gc
import argparse
from transformers import AutoModelForCausalLM, AutoTokenizer
from collections import OrderedDict

def human_size(nbytes):
    gb = 1024**3
    return f"{nbytes/gb:.2f} GB"

def gbps(nbytes, secs):
    return (nbytes / (1024**3)) / max(secs, 1e-9)

class RealModelBenchmark:
    def __init__(self, model_path="", device="cuda:0"):
        self.model_path = model_path
        self.device = device
        self.cpu_model = None
        self.model_size_bytes = 0
        
    def load_model_to_cpu(self):
        """将模型加载到CPU内存"""
        print(f"=== 加载模型到CPU: {self.model_path} ===")
        
        t0 = time.perf_counter()
        self.cpu_model = AutoModelForCausalLM.from_pretrained(
            self.model_path,
            dtype=torch.float16,
            device_map="cpu",  # 强制在CPU
            trust_remote_code=True
        )
        t1 = time.perf_counter()
        
        # 计算模型大小
        total_params = 0
        total_bytes = 0
        for param in self.cpu_model.parameters():
            total_params += param.numel()
            total_bytes += param.numel() * param.element_size()
        
        self.model_size_bytes = total_bytes
        
        print(f"CPU加载时间: {t1-t0:.2f}s")
        print(f"模型参数: {total_params:,}")
        print(f"模型大小: {human_size(total_bytes)}")
        print(f"权重数量: {len(list(self.cpu_model.parameters()))}")
        
        return total_bytes
    
    def benchmark_standard_to_gpu(self):
        """标准 .to(device) 方法"""
        print(f"\n--- 方法1: 标准 model.to(device) ---")
        
        if self.cpu_model is None:
            raise ValueError("请先调用 load_model_to_cpu()")
        
        # 深拷贝模型避免修改原始模型
        import copy
        model_copy = copy.deepcopy(self.cpu_model)
        
        torch.cuda.empty_cache()
        
        t0 = time.perf_counter()
        gpu_model = model_copy.to(self.device)
        torch.cuda.synchronize()
        t1 = time.perf_counter()
        
        transfer_time = t1 - t0
        bandwidth = gbps(self.model_size_bytes, transfer_time)
        
        print(f"传输时间: {transfer_time:.3f}s")
        print(f"传输带宽: {bandwidth:.2f} GB/s")
        print(f"相对理论值: {bandwidth/12.55*100:.1f}%")
        
        del model_copy, gpu_model
        torch.cuda.empty_cache()
        gc.collect()
        
        return bandwidth
    
    def benchmark_manual_weight_transfer(self):
        """手动逐个权重传输"""
        print(f"\n--- 方法2: 手动权重传输 ---")
        
        if self.cpu_model is None:
            raise ValueError("请先调用 load_model_to_cpu()")
        
        torch.cuda.empty_cache()
        
        # 收集所有权重
        cpu_weights = []
        weight_names = []
        for name, param in self.cpu_model.named_parameters():
            cpu_weights.append(param.detach())
            weight_names.append(name)
        
        print(f"权重总数: {len(cpu_weights)}")
        
        # 逐个传输
        t0 = time.perf_counter()
        gpu_weights = []
        for cpu_weight in cpu_weights:
            gpu_weight = cpu_weight.to(self.device, non_blocking=True)
            gpu_weights.append(gpu_weight)
        torch.cuda.synchronize()
        t1 = time.perf_counter()
        
        transfer_time = t1 - t0
        bandwidth = gbps(self.model_size_bytes, transfer_time)
        
        print(f"传输时间: {transfer_time:.3f}s")
        print(f"传输带宽: {bandwidth:.2f} GB/s")
        print(f"相对理论值: {bandwidth/12.55*100:.1f}%")
        
        del cpu_weights, gpu_weights
        torch.cuda.empty_cache()
        gc.collect()
        
        return bandwidth
    
    def benchmark_batch_weight_transfer(self, batch_size=10):
        """批量权重传输"""
        print(f"\n--- 方法3: 批量权重传输 (批大小{batch_size}) ---")
        
        if self.cpu_model is None:
            raise ValueError("请先调用 load_model_to_cpu()")
        
        torch.cuda.empty_cache()
        
        # 收集所有权重
        cpu_weights = []
        for param in self.cpu_model.parameters():
            cpu_weights.append(param.detach())
        
        print(f"权重总数: {len(cpu_weights)}")
        print(f"批次数: {(len(cpu_weights) + batch_size - 1) // batch_size}")
        
        # 分批传输
        t0 = time.perf_counter()
        
        for i in range(0, len(cpu_weights), batch_size):
            batch = cpu_weights[i:i+batch_size]
            for cpu_weight in batch:
                gpu_weight = cpu_weight.to(self.device, non_blocking=True)
                del gpu_weight  # 立即释放
        
        torch.cuda.synchronize()
        t1 = time.perf_counter()
        
        transfer_time = t1 - t0
        bandwidth = gbps(self.model_size_bytes, transfer_time)
        
        print(f"传输时间: {transfer_time:.3f}s")
        print(f"传输带宽: {bandwidth:.2f} GB/s")
        print(f"相对理论值: {bandwidth/12.55*100:.1f}%")
        
        del cpu_weights
        torch.cuda.empty_cache()
        gc.collect()
        
        return bandwidth
    
    def benchmark_streaming_by_layer(self):
        """按层流式传输"""
        print(f"\n--- 方法4: 按层流式传输 ---")
        
        if self.cpu_model is None:
            raise ValueError("请先调用 load_model_to_cpu()")
        
        torch.cuda.empty_cache()
        
        # 按模块分组
        modules = []
        for name, module in self.cpu_model.named_modules():
            if len(list(module.children())) == 0:  # 叶子模块
                if any(p.numel() > 0 for p in module.parameters()):
                    modules.append((name, module))
        
        print(f"模块总数: {len(modules)}")
        
        # 按模块传输
        t0 = time.perf_counter()
        
        for name, module in modules:
            for param in module.parameters():
                if param.numel() > 0:
                    gpu_param = param.detach().to(self.device, non_blocking=True)
                    del gpu_param
        
        torch.cuda.synchronize()
        t1 = time.perf_counter()
        
        transfer_time = t1 - t0
        bandwidth = gbps(self.model_size_bytes, transfer_time)
        
        print(f"传输时间: {transfer_time:.3f}s")
        print(f"传输带宽: {bandwidth:.2f} GB/s")
        print(f"相对理论值: {bandwidth/12.55*100:.1f}%")
        
        torch.cuda.empty_cache()
        gc.collect()
        
        return bandwidth
    
    def benchmark_state_dict_transfer(self):
        """通过 state_dict 传输"""
        print(f"\n--- 方法5: state_dict 传输 ---")
        
        if self.cpu_model is None:
            raise ValueError("请先调用 load_model_to_cpu()")
        
        torch.cuda.empty_cache()
        
        # 获取 state_dict
        state_dict = self.cpu_model.state_dict()
        print(f"state_dict 键数量: {len(state_dict)}")
        
        # 传输 state_dict
        t0 = time.perf_counter()
        
        gpu_state_dict = {}
        for key, tensor in state_dict.items():
            gpu_state_dict[key] = tensor.to(self.device, non_blocking=True)
        
        torch.cuda.synchronize()
        t1 = time.perf_counter()
        
        transfer_time = t1 - t0
        bandwidth = gbps(self.model_size_bytes, transfer_time)
        
        print(f"传输时间: {transfer_time:.3f}s")
        print(f"传输带宽: {bandwidth:.2f} GB/s")
        print(f"相对理论值: {bandwidth/12.55*100:.1f}%")
        
        del state_dict, gpu_state_dict
        torch.cuda.empty_cache()
        gc.collect()
        
        return bandwidth
    
    def analyze_weight_distribution(self):
        """分析权重分布"""
        print(f"\n=== 权重分布分析 ===")
        
        if self.cpu_model is None:
            raise ValueError("请先调用 load_model_to_cpu()")
        
        weight_sizes = []
        layer_info = {}
        
        for name, param in self.cpu_model.named_parameters():
            size_bytes = param.numel() * param.element_size()
            weight_sizes.append(size_bytes)
            
            # 按层类型分类
            layer_type = name.split('.')[0] if '.' in name else name
            if layer_type not in layer_info:
                layer_info[layer_type] = {'count': 0, 'total_bytes': 0}
            layer_info[layer_type]['count'] += 1
            layer_info[layer_type]['total_bytes'] += size_bytes
        
        print(f"权重数量: {len(weight_sizes)}")
        print(f"最大权重: {human_size(max(weight_sizes))}")
        print(f"最小权重: {human_size(min(weight_sizes))}")
        print(f"平均权重: {human_size(sum(weight_sizes)/len(weight_sizes))}")
        
        print(f"\n按层类型分布:")
        for layer_type, info in sorted(layer_info.items(), key=lambda x: x[1]['total_bytes'], reverse=True):
            percentage = info['total_bytes'] / self.model_size_bytes * 100
            print(f"  {layer_type:20s}: {info['count']:3d}个权重, {human_size(info['total_bytes'])}, {percentage:5.1f}%")

def main():
    parser = argparse.ArgumentParser(description="真实模型权重 H2D 性能测试")
    parser.add_argument("--model", default="Qwen/Qwen3-32B", help="模型路径")
    parser.add_argument("--device", default="cuda:0", help="GPU设备")
    parser.add_argument("--methods", default="all", 
                       help="测试方法: all, standard, manual, batch, stream, state_dict")
    parser.add_argument("--batch-size", type=int, default=10, help="批量传输的批大小")
    
    args = parser.parse_args()
    
    benchmark = RealModelBenchmark(args.model, args.device)
    
    # 加载模型到CPU
    model_size = benchmark.load_model_to_cpu()
    
    # 分析权重分布
    benchmark.analyze_weight_distribution()
    
    print(f"\n=== 开始H2D性能测试 ===")
    
    results = {}
    methods = args.methods.split(',') if args.methods != 'all' else [
        'standard', 'manual', 'batch', 'stream', 'state_dict'
    ]
    
    for method in methods:
        try:
            if method == 'standard':
                results['标准转移'] = benchmark.benchmark_standard_to_gpu()
            elif method == 'manual':
                results['手动权重'] = benchmark.benchmark_manual_weight_transfer()
            elif method == 'batch':
                results['批量传输'] = benchmark.benchmark_batch_weight_transfer(args.batch_size)
            elif method == 'stream':
                results['流式传输'] = benchmark.benchmark_streaming_by_layer()
            elif method == 'state_dict':
                results['state_dict'] = benchmark.benchmark_state_dict_transfer()
            
            # 清理和等待
            time.sleep(1)
            
        except Exception as e:
            print(f"方法 {method} 失败: {e}")
            continue
    
    # 性能总结
    print(f"\n=== 性能总结 ===")
    print(f"模型大小: {human_size(model_size)}")
    print(f"理论带宽: 12.55 GB/s")
    print(f"")
    
    for method, bandwidth in results.items():
        efficiency = bandwidth / 12.55 * 100
        print(f"{method:10s}: {bandwidth:5.2f} GB/s ({efficiency:5.1f}%)")
    
    if results:
        max_bandwidth = max(results.values())
        print(f"\n最佳方法带宽: {max_bandwidth:.2f} GB/s")
        print(f"H2D效率: {max_bandwidth/12.55*100:.1f}%")

if __name__ == "__main__":
    main()