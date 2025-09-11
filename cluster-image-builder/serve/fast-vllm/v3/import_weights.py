import os, time, gc, threading
import torch
import torch.nn as nn
from queue import Queue
from typing import List, Tuple
from transformers import AutoModelForCausalLM

from vllm.config import ModelConfig, VllmConfig, LoadConfig
from vllm.model_executor.model_loader import register_model_loader
from vllm.model_executor.model_loader.base_loader import BaseModelLoader
from vllm.model_executor.model_loader.utils import initialize_model, set_default_torch_dtype

import patch_v1_engine  # noqa: F401

# mock
from unittest.mock import patch
import vllm.distributed
import vllm.distributed.parallel_state as parallel_state

# os.environ.setdefault("VLLM_ENABLE_V1_MULTIPROCESSING", "0")

TARGET_DEVICE = torch.device(os.environ.get("MEMMAP_DEVICE", "cuda:0"))
BATCH_SIZE    = int(os.environ.get("MEMMAP_BATCH_SIZE", "20"))   # 复用 loading test 的批量大小
FORCE_DTYPE   = os.environ.get("MEMMAP_DTYPE", "").strip().lower() or None

def human_size(nbytes):
    gb = 1024**3
    return f"{nbytes/gb:.2f} GB"

def gbps(nbytes, secs):
    return (nbytes / (1024**3)) / max(secs, 1e-9)

# 全局预热状态
_global_cpu_model = None
_global_cpu_weights = None
_global_total_bytes = 0
_global_preheated_path = None

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

# Mock 并行相关函数
def mock_get_tensor_model_parallel_rank():
    return 0

def mock_get_tensor_model_parallel_world_size():
    return 1


@register_model_loader("memmap")
class FastMemmapLoader(BaseModelLoader):
    def __init__(self, load_config: LoadConfig):
        super().__init__(load_config)
        print(f"[FastMemmapLoader] 创建实例: {id(self)}")

    def preheat(self, vllm_config):
        """预热模型权重到CPU - 接收外部提供的完整vllm_config"""
        global _global_cpu_model, _global_cpu_weights, _global_total_bytes, _global_preheated_path
        
        model_path = vllm_config.model_config.model
        print(f"=== [{id(self)}] 预热模型: {model_path} ===")
        torch.cuda.empty_cache()
        
        t0 = time.perf_counter()
        
        # 创建vllm_config的独立副本，避免污染原始配置
        import copy
        
        print("创建vllm_config副本并初始化CPU模型...")
        preheat_vllm_config = copy.deepcopy(vllm_config)
        print(f"配置dtype: {preheat_vllm_config.model_config.dtype}")
        
        # 测试：使用AutoModelForCausalLM来对比性能差异
        USE_TRANSFORMERS_MODEL = os.environ.get("USE_TRANSFORMERS_MODEL", "0") == "1"
        
        if USE_TRANSFORMERS_MODEL:
            print("🔬 实验: 使用transformers AutoModelForCausalLM")
            cpu_model = AutoModelForCausalLM.from_pretrained(
                model_path,
                dtype=preheat_vllm_config.model_config.dtype,
                device_map="cpu",
                trust_remote_code=True
            )
        else:
            print("📊 使用vLLM initialize_model")
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
                       set_default_torch_dtype(preheat_vllm_config.model_config.dtype):
                    cpu_model = initialize_model(vllm_config=preheat_vllm_config)
        
        # 权重提取
        cpu_weights = [p.detach() for p in cpu_model.parameters()]
        total_bytes = sum(w.numel() * w.element_size() for w in cpu_weights)
        
        # 调试：检查权重数据类型和参数统计
        if cpu_weights:
            print(f"预热权重类型: {cpu_weights[0].dtype}")
            print(f"权重设备: {cpu_weights[0].device}")
            print(f"模型类型: {type(cpu_model).__name__}")
            print(f"参数数量: {len(cpu_weights)}")
            
            # 计算实际参数量
            total_params = sum(p.numel() for p in cpu_weights)
            print(f"总参数量: {total_params:,} ({total_params/1e9:.1f}B)")
        
        # 存储到全局状态 (保留CPU模型用于就地转换)
        _global_cpu_model = cpu_model
        _global_cpu_weights = cpu_weights
        _global_total_bytes = total_bytes
        _global_preheated_path = model_path
        
        # 不删除cpu_model，保留用于就地转换
        gc.collect()
        
        t1 = time.perf_counter()
        print(f"预热完成: {t1-t0:.3f}s, {human_size(total_bytes)}, {len(cpu_weights)}个权重")
        return {'time': t1-t0, 'bytes': total_bytes, 'count': len(cpu_weights)}
    
    def download_model(self, model_config: ModelConfig) -> None:
        return

    @torch.no_grad()  
    def load_weights(self, model: nn.Module, model_config: ModelConfig) -> None:
        """兼容接口 - 现在使用就地转换模式，此方法为空实现"""
        print("⚠️  load_weights被调用，但现在使用就地转换模式")
        return

    def load_model(self, vllm_config: VllmConfig, model_config: ModelConfig) -> nn.Module:
        global _global_cpu_model, _global_cpu_weights, _global_preheated_path
        
        model_path = model_config.model
        
        # 自动预热（如果未预热或路径不匹配）
        if not _global_cpu_weights:
            print("⚠️  模型未预热，自动进行预热...")
            self.preheat(vllm_config)
        elif _global_preheated_path != model_path:
            print(f"⚠️  路径不匹配 ({_global_preheated_path} vs {model_path})，重新预热...")
            self.preheat(vllm_config)
        else:
            print("✅ 使用已预热的权重")
        
        # 就地转换CPU模型到GPU (复制inference test的高性能模式)
        print("=== 就地转换CPU模型到GPU ===")
        self._transfer_cpu_model_to_gpu(_global_cpu_model)
        
        # 获取转换后的模型并清理全局状态
        model = _global_cpu_model
        # _global_cpu_model = None
        # _global_cpu_weights = None
        # _global_total_bytes = 0
        # _global_preheated_path = None
        # gc.collect()
        
        return model

    def _transfer_cpu_model_to_gpu(self, cpu_model):
        """就地将CPU模型转换到GPU - 完全复制inference test的高性能逻辑"""
        global _global_cpu_weights, _global_total_bytes
        
        if not _global_cpu_weights:
            raise ValueError("CPU权重未就绪")
            
        # 获取模型参数 (与inference test相同)
        params = list(cpu_model.parameters())
        
        print(f"=== GPU传输 (批大小={BATCH_SIZE}) ===")
        print(f"权重数量: {len(_global_cpu_weights)}")
        
        # 检查内存连续性
        # if _global_cpu_weights:
        #     first_weight = _global_cpu_weights[0]
        #     print(f"首个权重连续性: {first_weight.is_contiguous()}")
        #     print(f"首个权重stride: {first_weight.stride()}")
            
        # 批量传输 (完全复制inference test逻辑)
        t0 = time.perf_counter()
        with torch.no_grad():
            for i in range(0, len(_global_cpu_weights), BATCH_SIZE):
                batch = _global_cpu_weights[i:i+BATCH_SIZE]
                for j, w in enumerate(batch):
                    g = w.to(TARGET_DEVICE, non_blocking=True)
                    params[i + j].data = g  # 就地修改同一模型的参数
                torch.cuda.synchronize()
                
        t1 = time.perf_counter()
        bandwidth = gbps(_global_total_bytes, t1 - t0)
        print(f"传输: {t1-t0:.3f}s, {bandwidth:.2f}GB/s ({bandwidth/12.55*100:.1f}%效率)")

# 测试函数
def _test(model_path: str):
    from vllm import LLM
    from vllm.config import LoadConfig
    
    print("=== FastMemmapLoader 测试 ===")
    llm = LLM(model=model_path, load_format="memmap", enforce_eager=True, dtype=torch.float16)
    print("llm initialized")
    
    # 独立预热 - 需要外部提供vllm_config
    loader = FastMemmapLoader(LoadConfig(load_format="memmap"))
    loader.preheat(llm.llm_engine.vllm_config)
    
    # vLLM 加载（会使用预热的权重）
    t0 = time.time()
    print(f"vLLM 初始化: {time.time()-t0:.3f}s")
    
    # 激活
    print("vLLM 激活")
    llm.llm_engine.v1_vllm_engine_activate()
    
    # 推理测试
    # out = llm.generate(["Hello"])
    # print(f"输出: {out[0].outputs[0].text}")

if __name__ == "__main__":
    # 可以设置环境变量测试不同配置
    # MEMMAP_BATCH_SIZE=20 python import_weights.py
    _test("Qwen/Qwen3-14B")