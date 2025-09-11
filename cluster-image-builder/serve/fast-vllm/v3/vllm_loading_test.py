#!/usr/bin/env python3
"""
vLLM模型加载性能测试 - 基于inference test但使用vLLM模型
对比transformers模型和vLLM模型的传输性能差异
"""

import torch
import time
import gc
import argparse
from transformers import AutoModelForCausalLM, AutoTokenizer
from vllm.config import VllmConfig, ModelConfig, CacheConfig, ParallelConfig, SchedulerConfig, DeviceConfig, DecodingConfig, CompilationConfig
from vllm.model_executor.model_loader.utils import initialize_model, set_default_torch_dtype
from unittest.mock import patch
import vllm.distributed
import vllm.distributed.parallel_state as parallel_state

def human_size(nbytes):
    gb = 1024**3
    return f"{nbytes/gb:.2f} GB"

def gbps(nbytes, secs):
    return (nbytes / (1024**3)) / max(secs, 1e-9)

class MockGroupCoordinator:
    @property
    def is_first_rank(self):
        return True
    @property
    def is_last_rank(self):
        return True
    @property
    def rank_in_group(self):
        return 0
    @property
    def world_size(self):
        return 1

class VLLMLoadingTester:
    def __init__(self, model_path, device="cuda:0", batch_size=20, use_transformers=False):
        self.model_path = model_path
        self.device = device
        self.batch_size = batch_size
        self.use_transformers = use_transformers
        
    def create_minimal_vllm_config(self):
        """创建最小化的vLLM配置"""
        model_config = ModelConfig(
            model=self.model_path,
            dtype=torch.float16,
            seed=0,
            max_model_len=4096,  # 最小长度
            skip_tokenizer_init=True
        )
        
        # 创建必需的配置对象
        cache_config = CacheConfig(
            block_size=16,
            gpu_memory_utilization=0.9,
            swap_space=4,
            cache_dtype="auto"
        )
        
        parallel_config = ParallelConfig(
            pipeline_parallel_size=1,
            tensor_parallel_size=1,
            worker_use_ray=False,
            max_parallel_loading_workers=None,
            disable_custom_all_reduce=False,
            tokenizer_pool_size=0,
            tokenizer_pool_type="ray",
            tokenizer_pool_extra_config={},
            ray_workers_use_nsight=False,
            placement_group=None
        )
        
        scheduler_config = SchedulerConfig(
            max_num_batched_tokens=None,
            max_num_seqs=256,
            max_model_len=model_config.max_model_len,
            use_v2_block_manager=False,
            num_lookahead_slots=0,
            delay_factor=0.0,
            enable_chunked_prefill=False,
            max_num_on_the_fly=1,
            policy="fcfs",
        )
        
        device_config = DeviceConfig(device="cuda")
        
        decoding_config = DecodingConfig()
        
        compilation_config = CompilationConfig()
        
        return VllmConfig(
            model_config=model_config,
            cache_config=cache_config,
            parallel_config=parallel_config,
            scheduler_config=scheduler_config,
            device_config=device_config,
            lora_config=None,
            speculative_config=None,
            decoding_config=decoding_config,
            observability_config=None,
            prompt_adapter_config=None,
            quant_config=None,
            compilation_config=compilation_config,
        )
    
    def load_model_comparison(self):
        print(f"\n=== 模型加载对比测试: {self.model_path} ===")
        print(f"使用模型类型: {'transformers' if self.use_transformers else 'vLLM'}")

        if self.use_transformers:
            # 原始transformers方式
            print("\n--- transformers AutoModelForCausalLM ---")
            t0 = time.perf_counter()
            cpu_model = AutoModelForCausalLM.from_pretrained(
                self.model_path,
                dtype=torch.float16,
                device_map="cpu",
                trust_remote_code=True
            )
            t1 = time.perf_counter()
        else:
            # vLLM方式
            print("\n--- vLLM initialize_model ---")
            vllm_config = self.create_minimal_vllm_config()
            
            t0 = time.perf_counter()
            with torch.device("cpu"):
                mock_coordinator = MockGroupCoordinator()
                with patch.object(vllm.distributed, 'get_pp_group',
                                return_value=mock_coordinator), \
                     patch.object(vllm.distributed, 'get_tp_group',
                                return_value=mock_coordinator), \
                     patch.object(parallel_state, 'get_pp_group',
                                return_value=mock_coordinator), \
                     patch.object(parallel_state, 'get_tp_group',
                                return_value=mock_coordinator), \
                     set_default_torch_dtype(torch.float16):
                    cpu_model = initialize_model(vllm_config=vllm_config)
            t1 = time.perf_counter()

        cpu_load_time = t1 - t0

        # 权重提取和分析
        print("\n步骤2: 权重提取和分析...")
        t2 = time.perf_counter()
        params = list(cpu_model.parameters())
        cpu_weights = [p.detach() for p in params]
        total_bytes = sum(w.numel() * w.element_size() for w in cpu_weights)
        t3 = time.perf_counter()
        extract_time = t3 - t2

        print(f"CPU加载: {cpu_load_time:.3f}s")
        print(f"权重提取: {extract_time:.3f}s")
        print(f"模型大小: {human_size(total_bytes)}")
        print(f"模型类型: {type(cpu_model).__name__}")
        print(f"权重数量: {len(cpu_weights)}")
        
        # 计算参数量
        total_params = sum(p.numel() for p in cpu_weights)
        print(f"总参数量: {total_params:,} ({total_params/1e9:.1f}B)")

        # 详细权重分布分析
        print(f"\n权重分布分析:")
        
        # 更细化的大小分类
        tiny_weights = sum(1 for w in cpu_weights if w.numel() < 1000)         # <1K
        small_weights = sum(1 for w in cpu_weights if 1000 <= w.numel() < 100000)    # 1K-100K
        medium_weights = sum(1 for w in cpu_weights if 100000 <= w.numel() < 10000000)  # 100K-10M
        large_weights = sum(1 for w in cpu_weights if 10000000 <= w.numel() < 100000000)  # 10M-100M
        huge_weights = sum(1 for w in cpu_weights if w.numel() >= 100000000)   # >100M
        
        print(f"微小权重(<1K): {tiny_weights}")
        print(f"小权重(1K-100K): {small_weights}")  
        print(f"中权重(100K-10M): {medium_weights}")
        print(f"大权重(10M-100M): {large_weights}")
        print(f"巨大权重(>100M): {huge_weights}")
        
        # 最大权重分析
        max_weight_size = max(w.numel() for w in cpu_weights)
        max_weight_mb = max_weight_size * 2 / 1024 / 1024  # float16 = 2 bytes
        print(f"最大权重: {max_weight_size:,} 元素 ({max_weight_mb:.1f}MB)")
        
        # Top 10大权重分析
        weight_sizes = sorted([w.numel() for w in cpu_weights], reverse=True)[:10]
        print(f"Top 10权重大小:")
        for i, size in enumerate(weight_sizes):
            mb = size * 2 / 1024 / 1024
            print(f"  {i+1}. {size:,} 元素 ({mb:.1f}MB)")
        
        # 按大小统计总内存占比
        total_elements = sum(w.numel() for w in cpu_weights)
        tiny_elements = sum(w.numel() for w in cpu_weights if w.numel() < 1000)
        small_elements = sum(w.numel() for w in cpu_weights if 1000 <= w.numel() < 100000)
        medium_elements = sum(w.numel() for w in cpu_weights if 100000 <= w.numel() < 10000000)
        large_elements = sum(w.numel() for w in cpu_weights if 10000000 <= w.numel() < 100000000)
        huge_elements = sum(w.numel() for w in cpu_weights if w.numel() >= 100000000)
        
        print(f"\n内存占比:")
        print(f"微小权重: {tiny_elements/total_elements*100:.1f}%")
        print(f"小权重: {small_elements/total_elements*100:.1f}%")
        print(f"中权重: {medium_elements/total_elements*100:.1f}%")  
        print(f"大权重: {large_elements/total_elements*100:.1f}%")
        print(f"巨大权重: {huge_elements/total_elements*100:.1f}%")

        # 用户确认
        print("\n⏸️  准备开始GPU传输")
        input("按Enter继续...")

        # GPU传输测试
        print(f"\n步骤3: 批量传输测试 (批大小={self.batch_size})...")
        t4 = time.perf_counter()
        with torch.no_grad():
            for i in range(0, len(cpu_weights), self.batch_size):
                batch = cpu_weights[i:i+self.batch_size]
                for j, w in enumerate(batch):
                    g = w.to(self.device, non_blocking=True)
                    params[i + j].data = g
                torch.cuda.synchronize()
        t5 = time.perf_counter()
        transfer_time = t5 - t4
        bandwidth = gbps(total_bytes, transfer_time)
        
        print(f"传输时间: {transfer_time:.3f}s")
        print(f"传输带宽: {bandwidth:.2f} GB/s")
        print(f"硬件效率: {bandwidth/12.55*100:.1f}%")

        # 清理
        del cpu_model, cpu_weights, params
        gc.collect()
        
        return {
            'model_type': 'transformers' if self.use_transformers else 'vLLM',
            'cpu_load_time': cpu_load_time,
            'extract_time': extract_time,
            'transfer_time': transfer_time,
            'bandwidth': bandwidth,
            'total_bytes': total_bytes,
            'weight_count': len(cpu_weights),
            'tiny_weights': tiny_weights,
            'small_weights': small_weights,
            'medium_weights': medium_weights,
            'large_weights': large_weights,
            'huge_weights': huge_weights,
            'max_weight_size': max_weight_size,
            'top10_weights': weight_sizes
        }

def main():
    parser = argparse.ArgumentParser(description="vLLM vs transformers 加载性能对比")
    parser.add_argument("--model", default="Qwen/Qwen3-14B", help="模型路径")
    parser.add_argument("--device", default="cuda:0", help="GPU设备")
    parser.add_argument("--batch-size", type=int, default=20, help="批量大小")
    parser.add_argument("--both", action="store_true", help="同时测试两种方式")
    parser.add_argument("--transformers", action="store_true", help="使用transformers模型")
    
    args = parser.parse_args()
    
    if args.both:
        print("=== 同时对比两种模式 ===")
        
        # 测试transformers
        print("\n🔬 测试1: transformers模型")
        tester1 = VLLMLoadingTester(args.model, args.device, args.batch_size, use_transformers=True)
        result1 = tester1.load_model_comparison()
        
        # 清理后测试vLLM
        torch.cuda.empty_cache()
        gc.collect()
        
        print("\n🔬 测试2: vLLM模型") 
        tester2 = VLLMLoadingTester(args.model, args.device, args.batch_size, use_transformers=False)
        result2 = tester2.load_model_comparison()
        
        # 对比结果
        print(f"\n{'='*60}")
        print("📊 性能对比总结")
        print(f"{'='*60}")
        print(f"{'指标':<20} {'transformers':<15} {'vLLM':<15} {'差异'}")
        print(f"{'-'*60}")
        print(f"{'权重数量':<20} {result1['weight_count']:<15} {result2['weight_count']:<15} {result2['weight_count']/result1['weight_count']:.2f}x")
        print(f"{'传输带宽(GB/s)':<20} {result1['bandwidth']:<15.2f} {result2['bandwidth']:<15.2f} {result2['bandwidth']/result1['bandwidth']:.2f}x")
        print(f"{'传输时间(s)':<20} {result1['transfer_time']:<15.3f} {result2['transfer_time']:<15.3f} {result2['transfer_time']/result1['transfer_time']:.2f}x")
        print(f"{'硬件效率(%)':<20} {result1['bandwidth']/12.55*100:<15.1f} {result2['bandwidth']/12.55*100:<15.1f}")
        
        print(f"\n权重分布对比:")
        print(f"{'类型':<15} {'transformers':<15} {'vLLM':<15} {'差异'}")
        print(f"{'-'*55}")
        print(f"{'微小(<1K)':<15} {result1['tiny_weights']:<15} {result2['tiny_weights']:<15} {result2['tiny_weights']-result1['tiny_weights']:+d}")
        print(f"{'小(1K-100K)':<15} {result1['small_weights']:<15} {result2['small_weights']:<15} {result2['small_weights']-result1['small_weights']:+d}")
        print(f"{'中(100K-10M)':<15} {result1['medium_weights']:<15} {result2['medium_weights']:<15} {result2['medium_weights']-result1['medium_weights']:+d}")
        print(f"{'大(10M-100M)':<15} {result1['large_weights']:<15} {result2['large_weights']:<15} {result2['large_weights']-result1['large_weights']:+d}")
        print(f"{'巨大(>100M)':<15} {result1['huge_weights']:<15} {result2['huge_weights']:<15} {result2['huge_weights']-result1['huge_weights']:+d}")
        
        print(f"\n最大权重对比:")
        t_max_mb = result1['max_weight_size'] * 2 / 1024 / 1024
        v_max_mb = result2['max_weight_size'] * 2 / 1024 / 1024
        print(f"transformers最大权重: {result1['max_weight_size']:,} ({t_max_mb:.1f}MB)")
        print(f"vLLM最大权重: {result2['max_weight_size']:,} ({v_max_mb:.1f}MB)")
        print(f"差异: {result2['max_weight_size']/result1['max_weight_size']:.2f}x")
        
    else:
        # 单独测试
        use_transformers = args.transformers
        tester = VLLMLoadingTester(args.model, args.device, args.batch_size, use_transformers)
        result = tester.load_model_comparison()
        print(f"\n✅ 测试完成 - {result['model_type']}模型")

if __name__ == "__main__":
    main()