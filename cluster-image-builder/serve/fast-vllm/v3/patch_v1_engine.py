# patch_v1_engine.py
from typing import Optional, Tuple
import os
import time

import vllm.envs as envs
from vllm.config import VllmConfig
from vllm.v1.executor.abstract import Executor
from vllm.tasks import SupportedTask
from vllm.multimodal import MULTIMODAL_REGISTRY, MultiModalRegistry
from vllm.transformers_utils.tokenizer_group import init_tokenizer_from_configs
from vllm.v1.engine.output_processor import OutputProcessor
from vllm.v1.engine.processor import Processor
from vllm.v1.engine.core_client import EngineCoreClient
from vllm.v1.engine.llm_engine import LLMEngine as V1LLMEngine
from vllm.usage.usage_lib import UsageContext
from vllm.v1.metrics.loggers import PrometheusStatLogger, StatLoggerBase, StatLoggerFactory

# 1) 保存原方法，便于激活后还原
_original_get_supported_tasks = V1LLMEngine.get_supported_tasks

def _patched_get_supported_tasks(self) -> Tuple[SupportedTask, ...]:
    return ("dummy")

def _patched_init(
    self,
    vllm_config: VllmConfig,
    executor_class: type[Executor],
    log_stats: bool,
    usage_context: UsageContext = UsageContext.ENGINE_CONTEXT,
    stat_loggers: Optional[list[StatLoggerFactory]] = None,
    mm_registry: MultiModalRegistry = MULTIMODAL_REGISTRY,
    use_cached_outputs: bool = False,
    multiprocess_mode: bool = False,
) -> None:
    if not envs.VLLM_USE_V1:
        raise ValueError(
            "Using V1 LLMEngine, but envs.VLLM_USE_V1=False. "
            "Set env VLLM_USE_V1=1."
        )

    if stat_loggers is not None:
        raise NotImplementedError(
            "V1 LLMEngine 不支持传入自定义 StatLoggers。请用 V0 或移除此参数。"
        )

    self.vllm_config = vllm_config
    self.model_config = vllm_config.model_config
    self.cache_config = vllm_config.cache_config

    self.log_stats = log_stats
    self.stat_logger: Optional[StatLoggerBase] = None
    if self.log_stats:
        self.stat_logger = PrometheusStatLogger(vllm_config)

    parallel_config = vllm_config.parallel_config
    if not multiprocess_mode and parallel_config.data_parallel_size > 1:
        self.dp_group = parallel_config.stateless_init_dp_group()
    else:
        self.dp_group = None

    self.should_execute_dummy_batch = False

    # 你的“跳过 tokenizer”策略
    if self.model_config.skip_tokenizer_init:
        self.tokenizer = None
    else:
        self.tokenizer = init_tokenizer_from_configs(
            model_config=vllm_config.model_config,
            scheduler_config=vllm_config.scheduler_config,
            lora_config=vllm_config.lora_config,
        )

    self.processor = Processor(
        vllm_config=vllm_config, tokenizer=self.tokenizer, mm_registry=mm_registry
    )
    self.output_processor = OutputProcessor(self.tokenizer, log_stats=self.log_stats)

    # 保留以便激活
    self.executor_class = executor_class
    self.multiprocess_mode = multiprocess_mode

def _patched_activate(self) -> None:
    # 这里接入你自定义的 EngineCore / Executor
    self.engine_core = EngineCoreClient.make_client(
        multiprocess_mode=self.multiprocess_mode,
        asyncio_mode=False,
        vllm_config=self.vllm_config,
        executor_class=self.executor_class,
        log_stats=self.log_stats,
    )
    if not self.multiprocess_mode:
        self.model_executor = self.engine_core.engine_core.model_executor  # type: ignore
    self.reset_mm_cache()

    # 激活后恢复官方的 get_supported_tasks（可选）
    V1LLMEngine.get_supported_tasks = _original_get_supported_tasks

# —— 实际打补丁 —— #
V1LLMEngine.get_supported_tasks = _patched_get_supported_tasks
V1LLMEngine.__init__ = _patched_init
V1LLMEngine.v1_vllm_engine_activate = _patched_activate
