# patch_v1_async_engine.py
from typing import Optional, Tuple
import asyncio

import vllm.envs as envs
from vllm.config import VllmConfig
from vllm.v1.executor.abstract import Executor
from vllm.tasks import SupportedTask
from vllm.multimodal import MULTIMODAL_REGISTRY, MultiModalRegistry
from vllm.transformers_utils.config import maybe_register_config_serialize_by_value
from vllm.transformers_utils.tokenizer_group import init_tokenizer_from_configs
from vllm.v1.engine.output_processor import OutputProcessor
from vllm.v1.engine.processor import Processor
from vllm.v1.engine.core_client import EngineCoreClient
from vllm.v1.engine.async_llm import AsyncLLM
from vllm.usage.usage_lib import UsageContext
from vllm.v1.metrics.loggers import StatLoggerFactory, StatLoggerManager

# 1) 保存原方法，便于激活后还原
_original_get_supported_tasks = AsyncLLM.get_supported_tasks

async def _patched_get_supported_tasks(self) -> Tuple[SupportedTask, ...]:
    return ("dummy")

def _patched_init(
    self,
    vllm_config: VllmConfig,
    executor_class: type[Executor],
    log_stats: bool,
    usage_context: UsageContext = UsageContext.ENGINE_CONTEXT,
    mm_registry: MultiModalRegistry = MULTIMODAL_REGISTRY,
    use_cached_outputs: bool = False,
    log_requests: bool = True,
    start_engine_loop: bool = True,
    stat_loggers: Optional[list[StatLoggerFactory]] = None,
    client_addresses: Optional[dict[str, str]] = None,
    client_count: int = 1,
    client_index: int = 0,
) -> None:
    """
    AsyncLLM 分阶段初始化 - 第一阶段（轻量级）
    """
    if not envs.VLLM_USE_V1:
        raise ValueError(
            "Using V1 AsyncLLMEngine, but envs.VLLM_USE_V1=False. "
            "Set env VLLM_USE_V1=1."
        )

    # Ensure we can serialize custom transformer configs
    maybe_register_config_serialize_by_value()

    self.model_config = vllm_config.model_config
    self.vllm_config = vllm_config
    self.log_requests = log_requests
    self.log_stats = log_stats

    # 轻量级组件初始化
    if self.model_config.skip_tokenizer_init:
        self.tokenizer = None
    else:
        self.tokenizer = init_tokenizer_from_configs(
            model_config=vllm_config.model_config,
            scheduler_config=vllm_config.scheduler_config,
            lora_config=vllm_config.lora_config,
        )

    # Processor (converts Inputs --> EngineCoreRequests).
    self.processor = Processor(
        vllm_config=vllm_config,
        tokenizer=self.tokenizer,
        mm_registry=mm_registry,
    )

    # OutputProcessor (converts EngineCoreOutputs --> RequestOutput).
    self.output_processor = OutputProcessor(self.tokenizer, log_stats=self.log_stats)

    # 保存重量级初始化所需参数
    self.executor_class = executor_class
    self.start_engine_loop = start_engine_loop
    self.stat_loggers = stat_loggers
    self.client_addresses = client_addresses
    self.client_count = client_count
    self.client_index = client_index

    # 暂时设置为None，激活时再初始化
    self.engine_core = None
    self.logger_manager = None
    self.output_handler = None

def _patched_activate(self) -> None:
    """
    AsyncLLM 分阶段初始化 - 第二阶段（重量级激活）
    """
    # EngineCore (starts the engine in background process).
    self.engine_core = EngineCoreClient.make_async_mp_client(
        vllm_config=self.vllm_config,
        executor_class=self.executor_class,
        log_stats=self.log_stats,
        client_addresses=self.client_addresses,
        client_count=self.client_count,
        client_index=self.client_index,
    )

    # Loggers.
    if self.log_stats:
        self.logger_manager = StatLoggerManager(
            vllm_config=self.vllm_config,
            engine_idxs=self.engine_core.engine_ranks_managed,
            custom_stat_loggers=self.stat_loggers,
        )
        self.logger_manager.log_engine_initialized()

    # 不立即启动output_handler，保持原有的懒加载行为
    self.output_handler = None
    
    # 激活后恢复原方法
    AsyncLLM.get_supported_tasks = _original_get_supported_tasks

# 保持 _run_output_handler 原样，让自然错误发生

# 移除了过度复杂的装饰器检查机制

# —— 实际打补丁 —— #
AsyncLLM.get_supported_tasks = _patched_get_supported_tasks
AsyncLLM.__init__ = _patched_init
AsyncLLM.v1_async_llm_activate = _patched_activate

print("AsyncLLM patch applied successfully! Use v1_async_llm_activate() to activate after lightweight initialization.")