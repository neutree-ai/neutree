# stageable_engine.py
# SPDX-License-Identifier: Apache-2.0
# All-in-one stageable engine: executor + staged LLMEngine (+ async) + async wrapper + recycler.
# Requires vLLM (V0 path). No invasive change to vLLM repo needed.

import asyncio
import inspect
from enum import Enum
from dataclasses import dataclass, field
import os
import time
import gc
import ctypes
import glob
from ctypes.util import find_library
from functools import partial
from typing import (Any, AsyncGenerator, Callable, Dict, Iterable, List, Mapping,
                    Optional, Tuple, Type, Union, Set)

import torch

# ---------------- vLLM imports (v0 path) ----------------
import vllm.envs as envs
from vllm.config import (DecodingConfig, ObservabilityConfig, VllmConfig)
from vllm.engine.metrics_types import StatLoggerBase
from vllm.engine.llm_engine import LLMEngine, SchedulerOutputState, SchedulerContext
from vllm.engine.output_processor.interfaces import SequenceGroupOutputProcessor
from vllm.engine.output_processor.stop_checker import StopChecker
from vllm.entrypoints.openai.logits_processors import (
    get_logits_processors as get_openai_logits_processors)
from vllm.executor.executor_base import ExecutorBase
from vllm.executor.uniproc_executor import UniProcExecutor
from vllm.inputs import PromptType
from vllm.inputs.preprocess import InputPreprocessor
from vllm.logger import init_logger
from vllm.lora.request import LoRARequest
from vllm.model_executor.guided_decoding import (
    get_guided_decoding_logits_processor)
from vllm.model_executor.layers.sampler import SamplerOutput
from vllm.multimodal import MULTIMODAL_REGISTRY
from vllm.outputs import PoolingRequestOutput, RequestOutput
from vllm.pooling_params import PoolingParams
from vllm.prompt_adapter.request import PromptAdapterRequest
from vllm.sampling_params import SamplingParams
from vllm.sequence import ExecuteModelRequest
from vllm.transformers_utils.detokenizer import Detokenizer
from vllm.transformers_utils.tokenizer import AnyTokenizer
from vllm.transformers_utils.tokenizer_group import (
    TokenizerGroup, init_tokenizer_from_configs)
from vllm.usage.usage_lib import UsageContext, is_usage_stats_enabled, usage_message
from vllm.utils import (Counter, resolve_obj_by_qualname, weak_bind,
                        get_distributed_init_method, get_ip, get_open_port)
from vllm.version import __version__ as VLLM_VERSION
from vllm.engine.async_timeout import asyncio_timeout
from vllm.engine.async_llm_engine import (
    ENGINE_ITERATION_TIMEOUT_S, AsyncEngineDeadError, RequestTracker, STOP_ITERATION)
from vllm.worker.worker_base import WorkerWrapperBase

# 使用print替代logger来提升可观测性
print(f"[STAGEABLE_ENGINE] Module loading...")


# ======================================================================
# Stage constants
# ======================================================================
class EngineStage(str, Enum):
    UNINITIALIZED = "uninitialized"
    STAGE1_READY = "stage1_ready"
    STAGE2_ACTIVE = "stage2_active"
    STAGE2_COOLDOWN = "stage2_cooldown"
    ERROR = "error"

@dataclass
class EngineMetrics:
    stage1_started_at: float = 0.0
    stage1_completed_at: float = 0.0
    stage2_started_at: float = 0.0
    stage2_completed_at: float = 0.0
    stage1_time: float = 0.0
    stage2_time: float = 0.0

    total_requests: int = 0
    last_request_at: float | None = None

    @property
    def active_since(self) -> float | None:
        return self.stage2_completed_at or None

# ======================================================================
# Stageable UniProc Executor
# ======================================================================
class StageableUniProcExecutor(UniProcExecutor):
    """Single-process executor that skips model loading in Stage1."""

    uses_ray: bool = False

    def _init_executor(self) -> None:
        """Stage1: init worker & device, but DO NOT load model."""
        self.driver_worker = WorkerWrapperBase(vllm_config=self.vllm_config,
                                               rpc_rank=0)
        distributed_init_method = get_distributed_init_method(
            get_ip(), get_open_port())
        local_rank = 0
        device_info = self.vllm_config.device_config.device.__str__().split(":")
        if len(device_info) > 1:
            local_rank = int(device_info[1])
        rank = 0
        kwargs = dict(
            vllm_config=self.vllm_config,
            local_rank=local_rank,
            rank=rank,
            distributed_init_method=distributed_init_method,
            is_driver_worker=True,
        )
        self.collective_rpc("init_worker", args=([kwargs], ))
        self.collective_rpc("init_device")
        # NOTE: intentionally skipping load_model here.

    def load_model_now(self) -> None:
        """Stage2: actual weight loading."""
        self.collective_rpc("load_model")

    async def safe_shutdown(self) -> None:
        """Shutdown that tolerates sync or async impl."""
        shut = getattr(self, "shutdown", None)
        if callable(shut):
            print(f"[RECYCLER] Model executor safe_shutdown completed successfully")
            ret = shut()
            if inspect.isawaitable(ret):
                await ret


# ======================================================================
# Stageable LLM Engine (with async methods)
# ======================================================================
class StageableLLMEngine(LLMEngine):
    """LLMEngine variant with explicit Stage1/Stage2.
    Do NOT call parent __init__. Build fields manually and split init."""

    def __init__(
        self,
        vllm_config: VllmConfig,
        usage_context: UsageContext = UsageContext.ENGINE_CONTEXT,
        stat_loggers: Optional[Dict[str, StatLoggerBase]] = None,
        log_stats: bool = True,
    ) -> None:
        if envs.VLLM_USE_V1:
            raise ValueError("StageableLLMEngine only supports V0 path.")
        self.stage = EngineStage.UNINITIALIZED
        self.vllm_config = vllm_config

        # Mirror LLMEngine fields
        self.model_config = vllm_config.model_config
        self.cache_config = vllm_config.cache_config
        self.lora_config = vllm_config.lora_config
        self.parallel_config = vllm_config.parallel_config
        self.scheduler_config = vllm_config.scheduler_config
        self.device_config = vllm_config.device_config
        self.speculative_config = vllm_config.speculative_config
        self.load_config = vllm_config.load_config
        self.decoding_config = vllm_config.decoding_config or DecodingConfig()
        self.prompt_adapter_config = vllm_config.prompt_adapter_config
        self.observability_config = vllm_config.observability_config or ObservabilityConfig()

        self.log_stats = log_stats
        self.use_cached_outputs = False

        # Deferred init members
        self.tokenizer: Optional[TokenizerGroup] = None
        self.detokenizer: Optional[Detokenizer] = None
        self.input_preprocessor: Optional[InputPreprocessor] = None
        self.model_executor: Optional[ExecutorBase] = None

        self.seq_counter = Counter()
        self.generation_config_fields = self.model_config.try_get_generation_config()

        self.cached_scheduler_outputs: Optional[List[SchedulerOutputState]] = None
        self.scheduler_contexts: Optional[List[SchedulerContext]] = None
        self.async_callbacks: List[Callable] = []
        self.process_request_outputs_callback: Optional[Callable] = None
        self.scheduler = None
        self.stat_loggers = stat_loggers or {}
        self.tracer = None
        self.output_processor = None
        self.seq_id_to_seq_group: Dict[str, Any] = {}
        self._skip_scheduling_next_step = False
        self._errored_with: Optional[BaseException] = None
        self._usage_context = usage_context
        
        self.metrics = EngineMetrics()
        self._cooldown = False

    # ---------------- Stage 1 ----------------
    def initialize_stage1(self) -> bool:
        if self.stage != EngineStage.UNINITIALIZED:
            print(f"[STAGEABLE_ENGINE] Stage1 already executed or invalid state: {self.stage}")
            return False

        self.metrics.stage1_started_at = time.time()
        print(f"[STAGEABLE_ENGINE] Initializing Stage1 (no weights), v{VLLM_VERSION}")

        # Tokenizer & detokenizer
        if not self.model_config.skip_tokenizer_init:
            self.tokenizer = init_tokenizer_from_configs(
                model_config=self.model_config,
                scheduler_config=self.scheduler_config,
                lora_config=self.lora_config,
            )
            self.detokenizer = Detokenizer(self.tokenizer)
        else:
            self.tokenizer = None
            self.detokenizer = None

        # Input preprocessor
        self.input_preprocessor = InputPreprocessor(
            self.model_config, self.tokenizer, MULTIMODAL_REGISTRY
        )
        self._precompute_chat_template_formats()

        # Executor (staged)
        self.model_executor = StageableUniProcExecutor(vllm_config=self.vllm_config)

        self.stage = EngineStage.STAGE1_READY
        print(f"[STAGEABLE_ENGINE] Stage1 completed in {time.time() - self.metrics.stage1_started_at:.2f}s")
        self.metrics.stage1_completed_at = time.time()
        self.metrics.stage1_time = self.metrics.stage1_completed_at - self.metrics.stage1_started_at
        return True

    def _precompute_chat_template_formats(self):
        """触发一次 chat template 解析，让后续调用受益于内置的 @lru_cache"""
        if self.tokenizer is None:
            print("[STAGEABLE_ENGINE] Stage1 skip chat template precompute (tokenizer not initialized)")
            return
        
        try:
            # 导入所需函数
            from vllm.entrypoints.chat_utils import resolve_chat_template_content_format
            
            # 简单触发一次最常用的配置，让 @lru_cache 生效
            print("[STAGEABLE_ENGINE] Stage1 skip chat template precompute...")
            result = resolve_chat_template_content_format(
                chat_template=None,
                tools=None,
                given_format="auto",
                tokenizer=self.tokenizer,
                trust_remote_code=self.model_config.trust_remote_code,
            )
            print(f"[STAGEABLE_ENGINE] Stage1 Chat template precompute completed, detected format: {result}")

        except Exception as e:
            print(f"[STAGEABLE_ENGINE] Stage1 Chat template precompute failed: {e}")
            # 失败也不影响整体流程

    # ---------------- Stage 2 ----------------
    def initialize_stage2(self, new_engine_args: Optional['AsyncEngineArgs'] = None) -> bool:
        if self.stage != EngineStage.STAGE1_READY:
            print(f"[STAGEABLE_ENGINE] Stage2 requires Stage1; current state: {self.stage}")
            return False
        try:
            assert self.model_executor is not None
            self.metrics.stage2_started_at = time.time()
            
            # 0) switch model if needed
            # if new_engine_args:
                # self._switch_model(new_engine_args)
            
            # 1) load model
            load_now = getattr(self.model_executor, "load_model_now", None)
            if callable(load_now):
                load_now()
            else:
                # Fallback: if custom executor missing method, try standard load
                self.model_executor.collective_rpc("load_model")

            # 2) init KV cache (reuse parent logic)
            self._initialize_kv_caches()

            # 3) usage stats
            if is_usage_stats_enabled():
                from vllm.model_executor.model_loader import get_architecture_class_name
                usage_message.report_usage(
                    get_architecture_class_name(self.model_config),
                    self._usage_context,
                    extra_kvs={
                        "dtype": str(self.model_config.dtype),
                        "tensor_parallel_size": self.parallel_config.tensor_parallel_size,
                        "block_size": self.cache_config.block_size,
                        "gpu_memory_utilization": self.cache_config.gpu_memory_utilization,
                        "quantization": self.model_config.quantization,
                        "kv_cache_dtype": str(self.cache_config.cache_dtype),
                        "enable_lora": bool(self.lora_config),
                        "enable_prompt_adapter": bool(self.prompt_adapter_config),
                        "enable_prefix_caching": self.cache_config.enable_prefix_caching,
                        "enforce_eager": self.model_config.enforce_eager,
                        "disable_custom_all_reduce": self.parallel_config.disable_custom_all_reduce,
                    },
                )

            # 4) async callbacks scaffolding
            self.cached_scheduler_outputs = [
                SchedulerOutputState()
                for _ in range(self.parallel_config.pipeline_parallel_size)
            ]
            self.scheduler_contexts = [
                SchedulerContext(
                    multi_step_stream_outputs=self.scheduler_config.multi_step_stream_outputs
                )
                for _ in range(self.parallel_config.pipeline_parallel_size)
            ]
            if self.model_config.use_async_output_proc:
                process_model_outputs = weak_bind(self._process_model_outputs)
                self.async_callbacks = [
                    partial(process_model_outputs, ctx=self.scheduler_contexts[v_id])
                    for v_id in range(self.parallel_config.pipeline_parallel_size)
                ]
            else:
                self.async_callbacks = []
            self.process_request_outputs_callback = None

            # 5) build scheduler (same as parent)
            if isinstance(self.vllm_config.scheduler_config.scheduler_cls, str):
                Scheduler = resolve_obj_by_qualname(
                    self.vllm_config.scheduler_config.scheduler_cls)
            else:
                Scheduler = self.vllm_config.scheduler_config.scheduler_cls
            self.scheduler = [
                Scheduler(
                    self.scheduler_config, self.cache_config, self.lora_config,
                    self.parallel_config.pipeline_parallel_size,
                    self.async_callbacks[v_id]
                    if self.model_config.use_async_output_proc else None)
                for v_id in range(self.parallel_config.pipeline_parallel_size)
            ]

            # 6) metrics / tracing
            if self.log_stats and not self.stat_loggers:
                from vllm.engine.metrics import (LoggingStatLogger, PrometheusStatLogger)
                self.stat_loggers = {
                    "logging": LoggingStatLogger(local_interval=5, vllm_config=self.vllm_config),
                    "prometheus": PrometheusStatLogger(
                        local_interval=5,
                        labels=dict(model_name=self.model_config.served_model_name),
                        vllm_config=self.vllm_config),
                }
                self.stat_loggers["prometheus"].info("cache_config", self.cache_config)

            if self.observability_config.otlp_traces_endpoint:
                from vllm.tracing import init_tracer
                self.tracer = init_tracer("vllm.llm_engine",
                                          self.observability_config.otlp_traces_endpoint)

            # 7) output processor
            def get_tokenizer_for_seq(sequence):
                assert self.tokenizer is not None
                return self.tokenizer.get_lora_tokenizer(sequence.lora_request)

            self.output_processor = SequenceGroupOutputProcessor.create_output_processor(
                self.scheduler_config,
                self.detokenizer,
                self.scheduler,
                self.seq_counter,
                get_tokenizer_for_seq,
                stop_checker=StopChecker(self.scheduler_config.max_model_len, get_tokenizer_for_seq),
            )

            self.seq_id_to_seq_group = {}
            self._skip_scheduling_next_step = False

            self.metrics.stage2_completed_at = time.time()
            self.metrics.stage2_time = self.metrics.stage2_completed_at - self.metrics.stage2_started_at
            self._cooldown = False
            self.stage = EngineStage.STAGE2_ACTIVE
            print(f"[STAGEABLE_ENGINE] Stage2 completed in {self.metrics.stage2_time:.2f}s. Engine active.")
            return True
        except Exception as e:
            print(f"[STAGEABLE_ENGINE] Stage2 initialization failed: {e}")
            import traceback
            print(f"[STAGEABLE_ENGINE] Traceback: {traceback.format_exc()}")
            self.stage = EngineStage.ERROR
            self._errored_with = e
            return False

    def _switch_model(self, new_engine_args: 'AsyncEngineArgs'):
        original_model_path = self.model_config.model
        new_model_path = new_engine_args.model
        print(f"[STAGEABLE_ENGINE] Stage2 switch model: {original_model_path} -> {new_model_path}")
        
        try:
            from vllm.usage.usage_lib import UsageContext
            import dataclasses
            
            # 生成新的VllmConfig
            new_vllm_config = new_engine_args.create_engine_config(
                usage_context=UsageContext.ENGINE_CONTEXT
            )
            
            # 更新VllmConfig
            self.vllm_config = new_vllm_config
            
            # 更新引用
            self.model_config = self.vllm_config.model_config
            self.cache_config = self.vllm_config.cache_config
            self.lora_config = self.vllm_config.lora_config
            self.parallel_config = self.vllm_config.parallel_config
            self.scheduler_config = self.vllm_config.scheduler_config
            self.device_config = self.vllm_config.device_config
            self.speculative_config = self.vllm_config.speculative_config
            self.load_config = self.vllm_config.load_config
            self.decoding_config = self.vllm_config.decoding_config
            self.prompt_adapter_config = self.vllm_config.prompt_adapter_config
            self.observability_config = self.vllm_config.observability_config
            
            # 更新executor中的配置
            self.model_executor.vllm_config = self.vllm_config
            self.model_executor.driver_worker.vllm_config = self.vllm_config
            worker = self.model_executor.driver_worker.worker
            if worker:
                # 更新 worker 的配置
                worker.vllm_config = self.vllm_config
                worker.model_config = self.model_config

                # 更新 model_runner 的配置（这是关键）
                if hasattr(worker, 'model_runner') and worker.model_runner:
                    worker.model_runner.vllm_config = self.vllm_config
                    worker.model_runner.model_config = self.model_config

                    # 确保所有配置引用都更新
                    worker.model_runner.cache_config = self.cache_config
                    worker.model_runner.parallel_config = self.parallel_config
                    worker.model_runner.scheduler_config = self.scheduler_config
                    worker.model_runner.device_config = self.device_config

                    print(f"[STAGEABLE_ENGINE] Updated model_runner config to: {worker.model_runner.model_config.model}")

            # 如果需要重新初始化tokenizer（当模型路径改变时）
            if not self.model_config.skip_tokenizer_init:
                print("[STAGEABLE_ENGINE] Stage2 re-init Tokenizer...")
                from vllm.transformers_utils.tokenizer_group import init_tokenizer_from_configs
                self.tokenizer = init_tokenizer_from_configs(
                    model_config=self.model_config,
                    scheduler_config=self.scheduler_config,
                    lora_config=self.lora_config,
                )
                if self.tokenizer:
                    from vllm.transformers_utils.detokenizer import Detokenizer
                    self.detokenizer = Detokenizer(self.tokenizer)
                
                # 更新输入预处理器
                print("[STAGEABLE_ENGINE] Stage2 re-init InputPreprocessor...")
                from vllm.inputs.preprocess import InputPreprocessor
                from vllm.multimodal import MULTIMODAL_REGISTRY
                self.input_preprocessor = InputPreprocessor(
                    self.model_config,
                    self.tokenizer,
                    MULTIMODAL_REGISTRY
                )
            
            print(f"[STAGEABLE_ENGINE] Stage2 model switched to: {new_model_path}")
            
        except Exception as e:
            print(f"[STAGEABLE_ENGINE] Stage2 model switch failed: {e}")
            raise RuntimeError(f"model switch failed: {e}") from e

    def can_accept_requests(self) -> bool:
        # Active 或 Cooldown 都可继续服务（控制器零停机依赖此逻辑）
        return self.stage in (EngineStage.STAGE2_ACTIVE, EngineStage.STAGE2_COOLDOWN)

    def is_active(self) -> bool:
        return self.stage == EngineStage.STAGE2_ACTIVE

    def is_ready_for_activation(self) -> bool:
        return self.stage == EngineStage.STAGE1_READY

    def mark_cooldown(self) -> None:
        # 进入"可服务但准备回收"的阶段
        if self.stage == EngineStage.STAGE2_ACTIVE:
            self._cooldown = True
            self.stage = EngineStage.STAGE2_COOLDOWN
            print(f"[STAGEABLE_ENGINE] Engine marked as cooldown")

    def create_async_engine(self):
        """创建AsyncLLMEngine兼容的包装器"""
        if self.stage != EngineStage.STAGE2_ACTIVE:
            raise RuntimeError(f"Cannot create async engine from state: {self.stage}")
        
        # 创建一个StageableAsyncEngine实例来包装当前engine
        stageable_async = StageableAsyncEngine(
            vllm_config=self.vllm_config,
            usage_context=self._usage_context,
            start_engine_loop=True,  # 允许自动启动background loop
            log_requests=True,
        )
        # 替换内部engine为当前已激活的engine
        stageable_async.engine = self
        
        return AsyncEngineWrapper(stageable_async)
    
    def has_unfinished_requests(self) -> bool:
        """检查是否有未完成的请求"""
        if not hasattr(self, 'scheduler') or not self.scheduler:
            return False
        return any(sched.has_unfinished_seqs() for sched in self.scheduler)

    # ---------------- Guards ----------------
    def _ensure_active(self):
        if self.stage not in (EngineStage.STAGE2_ACTIVE, EngineStage.STAGE2_COOLDOWN):
            raise RuntimeError("Engine not ready for serving. Activate Stage2 first.")

    def add_request(self, *args, **kwargs):
        self._ensure_active()
        return super().add_request(*args, **kwargs)

    def step(self):
        self._ensure_active()
        return super().step()

    # ---------------- Async methods (copied/adapted from _AsyncLLMEngine) ----------------
    async def stop_remote_worker_execution_loop_async(self) -> None:
        """Stop the remote worker execution loop."""
        # vLLM executors provide async version; but fallback to sync if missing.
        meth = getattr(self.model_executor, "stop_remote_worker_execution_loop_async", None)
        if callable(meth):
            await meth()
        else:
            self.model_executor.stop_remote_worker_execution_loop()

    async def get_tokenizer_async(self,
                                  lora_request: Optional[LoRARequest] = None
                                  ) -> AnyTokenizer:
        return await (self.get_tokenizer_group().get_lora_tokenizer_async(lora_request))

    async def add_request_async(
        self,
        request_id: str,
        prompt: Optional[PromptType] = None,
        params: Optional[Union[SamplingParams, PoolingParams]] = None,
        arrival_time: Optional[float] = None,
        lora_request: Optional[LoRARequest] = None,
        trace_headers: Optional[Mapping[str, str]] = None,
        prompt_adapter_request: Optional[PromptAdapterRequest] = None,
        priority: int = 0,
        *,
        inputs: Optional[PromptType] = None,  # kept for API parity
    ) -> None:
        """Async version of add_request (requires Stage2 active)."""
        self._ensure_active()

        if inputs is not None:
            prompt = inputs
        assert prompt is not None and params is not None

        if lora_request is not None and not self.lora_config:
            raise ValueError(f"Got lora_request {lora_request} but LoRA is not enabled!")
        if priority != 0 and not self.scheduler_config.policy == "priority":
            raise ValueError(f"Got priority {priority} but Priority scheduling is not enabled.")
        if arrival_time is None:
            arrival_time = time.time()

        if self.tokenizer is not None:
            tokenizer = await self.get_tokenizer_async(lora_request)
            self._validate_token_prompt(prompt, tokenizer=tokenizer)

        processed_inputs = await self.input_preprocessor.preprocess_async(
            prompt,
            lora_request=lora_request,
            prompt_adapter_request=prompt_adapter_request,
        )

        if isinstance(params, SamplingParams) and params.guided_decoding is not None:
            # Guided decoding has an async implementation for building logits processors.
            params = await build_guided_decoding_logits_processor_async(
                sampling_params=params,
                tokenizer=await self.get_tokenizer_async(lora_request),
                default_guided_backend=self.decoding_config.guided_decoding_backend,
                reasoning_backend=self.decoding_config.reasoning_backend,
                model_config=self.model_config)

        self._add_processed_request(
            request_id=request_id,
            processed_inputs=processed_inputs,
            params=params,
            arrival_time=arrival_time,
            lora_request=lora_request,
            prompt_adapter_request=prompt_adapter_request,
            trace_headers=trace_headers,
            priority=priority,
        )

    async def step_async(
        self, virtual_engine: int
    ) -> List[Union[RequestOutput, PoolingRequestOutput]]:
        """One async decoding iteration (requires Stage2 active)."""
        self._ensure_active()

        # cached outputs from previous iteration
        cached_outputs = self.cached_scheduler_outputs[virtual_engine]
        seq_group_metadata_list = cached_outputs.seq_group_metadata_list
        scheduler_outputs = cached_outputs.scheduler_outputs
        allow_async_output_proc = cached_outputs.allow_async_output_proc

        ctx = self.scheduler_contexts[virtual_engine]
        ctx.request_outputs.clear()

        # schedule if no remaining steps
        if not self._has_remaining_steps(seq_group_metadata_list):
            (seq_group_metadata_list, scheduler_outputs,
             allow_async_output_proc) = self.scheduler[virtual_engine].schedule()

            ctx.seq_group_metadata_list = seq_group_metadata_list
            ctx.scheduler_outputs = scheduler_outputs

            if not scheduler_outputs.is_empty():
                # NOTE: keep last finished ids handling parity with upstream
                finished_requests_ids = self.scheduler[
                    virtual_engine].get_and_reset_finished_requests_ids()
            else:
                finished_requests_ids = []

            # Switch async->sync if needed
            if not allow_async_output_proc and len(ctx.output_queue) > 0:
                self._process_model_outputs(ctx=ctx)

            if (self.scheduler_config.is_multi_step
                    and scheduler_outputs.num_lookahead_slots > 0):
                self._cache_scheduler_outputs_for_multi_step(
                    virtual_engine, seq_group_metadata_list, scheduler_outputs,
                    allow_async_output_proc)
        else:
            finished_requests_ids = list()

        assert seq_group_metadata_list is not None
        assert scheduler_outputs is not None

        if not scheduler_outputs.is_empty():
            last_sampled_token_ids = self._get_last_sampled_token_ids(virtual_engine)

            execute_model_req = ExecuteModelRequest(
                seq_group_metadata_list=seq_group_metadata_list,
                blocks_to_swap_in=scheduler_outputs.blocks_to_swap_in,
                blocks_to_swap_out=scheduler_outputs.blocks_to_swap_out,
                blocks_to_copy=scheduler_outputs.blocks_to_copy,
                virtual_engine=virtual_engine,
                num_lookahead_slots=scheduler_outputs.num_lookahead_slots,
                running_queue_size=scheduler_outputs.running_queue_size,
                finished_requests_ids=finished_requests_ids,
                last_sampled_token_ids=last_sampled_token_ids)

            if allow_async_output_proc:
                execute_model_req.async_callback = self.async_callbacks[virtual_engine]

            # Execute the model (async)
            outputs = await self.model_executor.execute_model_async(execute_model_req)

            if self.scheduler_config.is_multi_step:
                self._update_cached_scheduler_output(virtual_engine, outputs)
        else:
            if len(ctx.output_queue) > 0:
                self._process_model_outputs(ctx=ctx)
            outputs = []

        # Finish current step for all sequence groups (multi-step)
        if self.scheduler_config.is_multi_step:
            for seq_group in seq_group_metadata_list:
                seq_group.finish_step()

        if not self._has_remaining_steps(seq_group_metadata_list):
            # Clear cache if finished
            if self.scheduler_config.is_multi_step:
                self.cached_scheduler_outputs[virtual_engine] = SchedulerOutputState()

            # is_first_step_output for multi-step
            is_first_step_output: bool = False if not seq_group_metadata_list \
                else seq_group_metadata_list[0].state.num_steps == 1

            ctx.append_output(outputs=outputs,
                              seq_group_metadata_list=seq_group_metadata_list,
                              scheduler_outputs=scheduler_outputs,
                              is_async=allow_async_output_proc,
                              is_last_step=True,
                              is_first_step_output=is_first_step_output)

            if outputs and allow_async_output_proc:
                assert len(outputs) == 1, "Async postprocessor expects only a single output set"
                self._advance_to_next_step(
                    outputs[0], seq_group_metadata_list,
                    scheduler_outputs.scheduled_seq_groups)

            if not allow_async_output_proc:
                self._process_model_outputs(ctx=ctx)
                # Log stats & tracing
                self.do_log_stats(scheduler_outputs, outputs)
                self.do_tracing(scheduler_outputs)
        else:
            # Multi-step case
            return ctx.request_outputs

        if not self.has_unfinished_requests():
            # Drain async postprocessor if exists
            if len(ctx.output_queue) > 0:
                self._process_model_outputs(ctx=ctx)
            assert len(ctx.output_queue) == 0

        return ctx.request_outputs

    async def check_health_async(self) -> None:
        self.model_executor.check_health()

    async def collective_rpc_async(self,
                                   method: str,
                                   timeout: Optional[float] = None,
                                   args: tuple = (),
                                   kwargs: Optional[dict] = None):
        raise NotImplementedError

# Guided decoding async builder (copied from async_llm_engine.py)
async def build_guided_decoding_logits_processor_async(
        sampling_params: SamplingParams, tokenizer: AnyTokenizer,
        default_guided_backend: str, reasoning_backend: Optional[str],
        model_config) -> SamplingParams:
    if sampling_params.guided_decoding is None:
        return sampling_params
    sampling_params = sampling_params.clone()
    guided_decoding = sampling_params.guided_decoding
    print(f"[STAGEABLE_ENGINE] Building guided decoding logits processor. guided_decoding: {guided_decoding}" + 
          (f", reasoning_backend: {reasoning_backend}" if reasoning_backend else ""))
    guided_decoding.backend = guided_decoding.backend or default_guided_backend
    processor = await get_guided_decoding_logits_processor(
        guided_params=guided_decoding,
        tokenizer=tokenizer,
        reasoning_backend=reasoning_backend,
        model_config=model_config)
    if processor:
        if sampling_params.logits_processors is None:
            sampling_params.logits_processors = []
        sampling_params.logits_processors.append(processor)
    sampling_params.guided_decoding = None
    return sampling_params


# ======================================================================
# Stageable Async Engine (light wrapper)
# ======================================================================
class StageableAsyncEngine:
    """Light async wrapper that allows Stage1 parking & later Stage2 activation."""

    def __init__(
        self,
        vllm_config: VllmConfig,
        usage_context: UsageContext = UsageContext.ENGINE_CONTEXT,
        stat_loggers: Optional[Dict[str, StatLoggerBase]] = None,
        start_engine_loop: bool = True,
        log_requests: bool = True,
        log_stats: bool = True,
    ) -> None:
        self.log_requests = log_requests
        self.start_engine_loop = start_engine_loop
        self._errored_with: Optional[BaseException] = None

        # Build staged engine and run Stage1 only
        self.engine = StageableLLMEngine(
            vllm_config=vllm_config,
            usage_context=usage_context,
            stat_loggers=stat_loggers,
            log_stats=log_stats,
        )

        self._request_tracker: Optional[RequestTracker] = None
        self._background_loop_unshielded: Optional[asyncio.Task] = None
        self.background_loop: Optional[asyncio.Future] = None

    # Lifecycle
    @property
    def is_running(self) -> bool:
        return (self.background_loop is not None
                and self._background_loop_unshielded is not None
                and not self._background_loop_unshielded.done())

    @property
    def is_stopped(self) -> bool:
        return self.errored or (self.background_loop is not None and
                                self._background_loop_unshielded is not None
                                and self._background_loop_unshielded.done())

    @property
    def errored(self) -> bool:
        return self._errored_with is not None

    @property
    def is_active(self) -> bool:
        return self.engine.stage == EngineStage.STAGE2_ACTIVE

    def set_errored(self, exc: Exception) -> None:
        self._errored_with = exc
        if self._request_tracker:
            self._request_tracker.propagate_exception(exc)

    def start_background_loop(self) -> None:
        if self.is_running:
            return
        self._request_tracker = RequestTracker()
        loop = asyncio.get_event_loop()
        self._background_loop_unshielded = loop.create_task(self._run_engine_loop())
        self.background_loop = asyncio.shield(self._background_loop_unshielded)


    async def _run_engine_loop(self) -> None:
        """主要的引擎循环，基于vLLM的run_engine_loop实现"""
        if not self.engine.can_accept_requests():
            print(f"[STAGEABLE_ENGINE] Engine not ready for serving")
            return
        
        pipeline_parallel_size = 1  # StageableLLMEngine目前只支持单GPU
        has_requests_in_progress = [False] * pipeline_parallel_size
        
        while self.engine.can_accept_requests() and not self._errored_with:
            try:
                if not any(has_requests_in_progress):
                    print(f"[STAGEABLE_ENGINE] Waiting for new requests...")
                    await self._request_tracker.wait_for_new_requests()
                    if self._errored_with:
                        break
                    print(f"[STAGEABLE_ENGINE] Got new requests!")
                    requests_in_progress = [
                        asyncio.create_task(self._engine_step(ve))
                        for ve in range(pipeline_parallel_size)
                    ]
                    has_requests_in_progress = [True] * pipeline_parallel_size

                # 处理请求
                async with asyncio_timeout(ENGINE_ITERATION_TIMEOUT_S):
                    done, _ = await asyncio.wait(
                        requests_in_progress,
                        return_when=asyncio.FIRST_COMPLETED)
                    
                for task in done:
                    result = task.result()
                    virtual_engine = requests_in_progress.index(task)
                    has_unfinished_requests = self.engine.has_unfinished_requests()
                    
                    if result or has_unfinished_requests:
                        requests_in_progress[virtual_engine] = (
                            asyncio.create_task(self._engine_step(virtual_engine)))
                        has_requests_in_progress[virtual_engine] = True
                    else:
                        has_requests_in_progress[virtual_engine] = False
                        
                await asyncio.sleep(0)
                
            except asyncio.CancelledError:
                break
            except asyncio.TimeoutError as exc:
                print(f"[STAGEABLE_ENGINE] Engine iteration timed out. This should never happen!")
                self.set_errored(exc)
                raise
            except Exception as e:
                print(f"[STAGEABLE_ENGINE] Engine loop error: {e}")
                import traceback
                print(f"[STAGEABLE_ENGINE] Traceback: {traceback.format_exc()}")
                self.set_errored(e)
                break

    async def _engine_step(self, virtual_engine: int) -> bool:
        """处理一个引擎步骤"""
        new_requests, aborted_requests = (
            self._request_tracker.get_new_and_aborted_requests())

        for new_request in new_requests:
            try:
                await self.engine.add_request_async(**new_request)
            except ValueError as e:
                self._request_tracker.process_exception(
                    new_request["request_id"],
                    e,
                    verbose=self.log_requests,
                )

        if aborted_requests:
            self.engine.abort_request(aborted_requests)

        request_outputs = await self.engine.step_async(virtual_engine)

        # 处理输出
        all_finished = True
        for request_output in request_outputs:
            self._request_tracker.process_request_output(
                request_output, verbose=self.log_requests)
            all_finished = all_finished and request_output.finished

        return not all_finished

    async def shutdown_async(self) -> None:
        """异步关闭引擎"""
        # 停止后台循环
        if self._background_loop_unshielded and not self._background_loop_unshielded.done():
            self._background_loop_unshielded.cancel()
            try:
                await self._background_loop_unshielded
            except asyncio.CancelledError:
                pass

        # 关闭引擎组件
        if hasattr(self.engine, 'model_executor') and self.engine.model_executor:
            await self.engine.model_executor.safe_shutdown()

    def create_async_engine(self):
        """创建AsyncLLMEngine兼容的包装器"""
        if self.engine.stage != EngineStage.STAGE2_ACTIVE:
            raise RuntimeError(f"Cannot create async engine from state: {self.engine.stage}")
        
        return AsyncEngineWrapper(self)


# ======================================================================
# AsyncLLMEngine兼容包装器
# ======================================================================
class AsyncEngineWrapper:
    """提供AsyncLLMEngine兼容接口的包装器"""
    
    def __init__(self, stageable_async_engine: StageableAsyncEngine):
        self.stageable_engine = stageable_async_engine
        self.engine = stageable_async_engine.engine
    
    @property
    def errored(self) -> bool:
        """检查引擎是否出错"""
        return self.stageable_engine.errored
    
    @property
    def dead_error(self) -> Optional[BaseException]:
        """获取导致引擎死亡的错误"""
        return self.stageable_engine._errored_with

    async def shutdown(self) -> None:
        """关闭引擎"""
        await self.stageable_engine.shutdown_async()

    # ============================================================================
    # vLLM OpenAI serving组件需要的方法
    # ============================================================================
    
    async def get_tokenizer(self, lora_request: Optional[LoRARequest] = None) -> AnyTokenizer:
        """获取tokenizer"""
        return await self.engine.get_tokenizer_async(lora_request)
    
    async def get_vllm_config(self) -> VllmConfig:
        """获取vLLM配置"""
        return self.engine.vllm_config
    
    async def get_model_config(self):
        """获取模型配置"""
        return self.engine.model_config
    
    async def get_parallel_config(self):
        """获取并行配置"""
        return self.engine.parallel_config
    
    async def get_scheduler_config(self):
        """获取调度器配置"""
        return self.engine.scheduler_config
    
    async def get_decoding_config(self):
        """获取解码配置"""
        return self.engine.decoding_config
    
    async def get_lora_config(self):
        """获取LoRA配置"""
        return self.engine.lora_config
    
    async def check_health(self) -> None:
        """健康检查"""
        await self.engine.check_health_async()
    
    async def get_input_preprocessor(self):
        """获取输入预处理器"""
        return self.engine.input_preprocessor
    
    def get_tokenizer_group(self):
        """获取tokenizer组"""
        return self.engine.tokenizer
    
    # 对于一些同步方法，直接代理到engine
    def get_vllm_config_sync(self) -> VllmConfig:
        """同步获取vLLM配置"""
        return self.engine.vllm_config
    
    def get_model_config_sync(self):
        """同步获取模型配置"""
        return self.engine.model_config
    
    # ============================================================================
    # 生成相关方法
    # ============================================================================
    
    async def generate(
        self,
        prompt: PromptType,
        sampling_params: SamplingParams,
        request_id: str,
        lora_request: Optional[LoRARequest] = None,
        trace_headers: Optional[Mapping[str, str]] = None,
        prompt_adapter_request: Optional[PromptAdapterRequest] = None,
        priority: int = 0,
    ) -> AsyncGenerator[RequestOutput, None]:
        """生成文本输出"""
        # 确保engine已经激活到Stage2
        if self.engine.stage != EngineStage.STAGE2_ACTIVE:
            raise RuntimeError(f"Engine not in STAGE2_ACTIVE state: {self.engine.stage}")
        
        # 启动后台循环如果还没有运行
        if not self.stageable_engine.is_running:
            if self.stageable_engine.start_engine_loop:
                self.stageable_engine.start_background_loop()
            else:
                raise AsyncEngineDeadError(
                    "Background loop is not running. Engine not properly activated.")
        
        # 确保request_tracker已经初始化
        if self.stageable_engine._request_tracker is None:
            raise RuntimeError("Request tracker not initialized. Engine may not be properly activated.")
        
        # 添加请求并返回生成器
        stream = self.stageable_engine._request_tracker.add_request(
            request_id,
            verbose=self.stageable_engine.log_requests,
            prompt=prompt,
            params=sampling_params,
            arrival_time=time.time(),
            lora_request=lora_request,
            trace_headers=trace_headers,
            prompt_adapter_request=prompt_adapter_request,
            priority=priority,
        )
        
        try:
            async for output in stream.generator():
                yield output
        except asyncio.CancelledError:
            await self.abort(request_id)
            raise
    
    async def encode(
        self,
        prompt: PromptType,
        pooling_params: PoolingParams,
        request_id: str,
        lora_request: Optional[LoRARequest] = None,
        trace_headers: Optional[Mapping[str, str]] = None,
        priority: int = 0,
    ) -> AsyncGenerator[PoolingRequestOutput, None]:
        """生成embedding输出"""
        # 确保engine已经激活到Stage2
        if self.engine.stage != EngineStage.STAGE2_ACTIVE:
            raise RuntimeError(f"Engine not in STAGE2_ACTIVE state: {self.engine.stage}")
        
        # 启动后台循环如果还没有运行
        if not self.stageable_engine.is_running:
            if self.stageable_engine.start_engine_loop:
                self.stageable_engine.start_background_loop()
            else:
                raise AsyncEngineDeadError(
                    "Background loop is not running. Engine not properly activated.")
        
        # 确保request_tracker已经初始化
        if self.stageable_engine._request_tracker is None:
            raise RuntimeError("Request tracker not initialized. Engine may not be properly activated.")
        
        # 添加请求并返回生成器
        stream = self.stageable_engine._request_tracker.add_request(
            request_id,
            verbose=self.stageable_engine.log_requests,
            prompt=prompt,
            params=pooling_params,
            arrival_time=time.time(),
            lora_request=lora_request,
            trace_headers=trace_headers,
            priority=priority,
        )
        
        try:
            async for output in stream.generator():
                yield output
        except asyncio.CancelledError:
            await self.abort(request_id)
            raise
    
    async def abort(self, request_id: str) -> None:
        """中止请求"""
        if not self.stageable_engine.is_running:
            raise AsyncEngineDeadError(
                "Background loop is not running.")
        
        # 确保request_tracker已经初始化
        if self.stageable_engine._request_tracker is None:
            raise RuntimeError("Request tracker not initialized.")
        
        self.stageable_engine._request_tracker.abort_request(
            request_id,
            exception=asyncio.CancelledError,
            verbose=self.stageable_engine.log_requests
        )


# ======================================================================
# 资源回收器
# ======================================================================
class EngineRecycler:
    """Engine资源回收器，实现彻底的资源清理"""
    
    @staticmethod
    async def recycle_engine(engine: StageableLLMEngine, force: bool = False) -> bool:
        """执行分层回收策略"""
        if engine.stage == EngineStage.ERROR:
            print(f"[RECYCLER] Engine is in error state, forcing cleanup")
            force = True
        
        print(f"[RECYCLER] Starting engine recycling (force={force})")
        
        try:
            # Level 1: 优雅停止
            if not force and hasattr(engine, 'model_executor') and engine.model_executor:
                print(f"[RECYCLER] Level 1: Graceful shutdown")
                try:
                    await engine.model_executor.safe_shutdown()
                except Exception as e:
                    print(f"[RECYCLER] Graceful shutdown failed: {e}")
            
            # Level 2: 强制清理组件
            print(f"[RECYCLER] Level 2: Force cleanup components")
            EngineRecycler._cleanup_components(engine)
            
            return True
            
        except Exception as e:
            print(f"[RECYCLER] Engine recycling failed: {e}")
            import traceback
            print(f"[RECYCLER] Traceback: {traceback.format_exc()}")
            return False
    
    @staticmethod
    def _cleanup_components(engine: StageableLLMEngine):
        """清理engine组件"""
        # 清理model executor
        if hasattr(engine, 'model_executor') and engine.model_executor:
            del engine.model_executor
            engine.model_executor = None
        
        # 清理tokenizer组件
        if hasattr(engine, 'tokenizer') and engine.tokenizer:
            del engine.tokenizer
            engine.tokenizer = None
        
        if hasattr(engine, 'detokenizer') and engine.detokenizer:
            del engine.detokenizer
            engine.detokenizer = None
        
        if hasattr(engine, 'input_preprocessor') and engine.input_preprocessor:
            del engine.input_preprocessor
            engine.input_preprocessor = None
        
        # Python垃圾回收
        gc.collect()
    
    @staticmethod
    def _reset_cuda_device():
        """重置CUDA设备"""
        if not torch.cuda.is_available() or not torch.cuda.is_initialized():
            return
        
        try:
            device = torch.cuda.current_device()
            torch.cuda.synchronize(device)
            torch.cuda.empty_cache()
            
            if hasattr(torch.cuda, "ipc_collect"):
                torch.cuda.ipc_collect()
            
            # 执行cudaDeviceReset
            libcudart = EngineRecycler._load_cudart_cdll()
            libcudart.cudaDeviceReset.restype = ctypes.c_int
            err = libcudart.cudaDeviceReset()
            
            if err != 0:
                raise RuntimeError(f"cudaDeviceReset failed with code {err}")
            
            print(f"[RECYCLER] CUDA device {device} reset successfully")
            
        except Exception as e:
            print(f"[RECYCLER] CUDA device reset failed: {e}")
    
    @staticmethod
    def _system_cleanup():
        """系统级清理"""
        try:
            # 释放glibc缓存
            libc = ctypes.CDLL("libc.so.6")
            libc.malloc_trim(0)
            
            # 最终垃圾回收
            gc.collect()
            
        except Exception as e:
            print(f"[RECYCLER] System cleanup failed: {e}")
    
    @staticmethod
    def _load_cudart_cdll() -> ctypes.CDLL:
        """加载CUDA runtime库"""
        name = find_library("cudart")
        candidates = []
        
        if name:
            candidates.append(name)
        
        candidates += ["libcudart.so.12", "libcudart.so.11", "libcudart.so"]
        
        if os.environ.get("CONDA_PREFIX"):
            candidates += glob.glob(os.path.join(os.environ["CONDA_PREFIX"], "lib", "libcudart.so*"))
        
        candidates += glob.glob("/usr/local/cuda*/lib64/libcudart.so*")
        candidates += glob.glob("/usr/lib/x86_64-linux-gnu/libcudart.so*")
        
        last_err = None
        for path in candidates:
            try:
                return ctypes.CDLL(path)
            except Exception as e:
                last_err = e
        
        raise OSError(f"Could not load libcudart from candidates={candidates}: {last_err}")


# ======================================================================
# 工厂函数和实用工具
# ======================================================================
def create_async_engine_args(
    model: str,
    dtype: str = "half",
    enforce_eager: bool = True,
    gpu_memory_utilization: float = 0.9,
    load_format: str = "auto",
    model_loader_extra_config: Optional[Dict] = None,
    **kwargs
) -> 'AsyncEngineArgs':
    """创建 AsyncEngineArgs 的工具方法"""
    from vllm.engine.arg_utils import AsyncEngineArgs
    
    # 过滤掉不被AsyncEngineArgs支持的参数
    unsupported_keys = [
        'tensorizer_uri', 'tensorizer_path', 'num_readers', 
        's3_endpoint', 's3_access_key_id', 's3_secret_access_key'
    ]
    
    filtered_kwargs = {}
    for key, value in kwargs.items():
        if key not in unsupported_keys:
            filtered_kwargs[key] = value
        else:
            print(f"[STAGEABLE_ENGINE] Filtering unsupported AsyncEngineArgs parameter: {key}")
    
    return AsyncEngineArgs(
        model=model,
        dtype=dtype,
        enforce_eager=enforce_eager,
        gpu_memory_utilization=gpu_memory_utilization,
        load_format=load_format,
        model_loader_extra_config=model_loader_extra_config,
        swap_space=0,  # 禁用swap space以优化CPU cache时间
        **filtered_kwargs
    )


def create_stageable_engine_from_args(engine_args: 'AsyncEngineArgs') -> StageableLLMEngine:
    """从 AsyncEngineArgs 创建分阶段引擎"""
    from vllm.usage.usage_lib import UsageContext
    
    # 创建vLLM配置
    vllm_config = engine_args.create_engine_config(
        usage_context=UsageContext.ENGINE_CONTEXT
    )
    
    return StageableLLMEngine(vllm_config)

def create_stageable_engine(
    model: str,
    dtype: str = "half",
    enforce_eager: bool = True,
    gpu_memory_utilization: float = 0.9,
    load_format: str = "auto",
    model_loader_extra_config: Optional[Dict] = None,
    **kwargs
) -> StageableLLMEngine:
    """创建分阶段引擎的工厂函数"""
    
    # 使用新的工具方法
    engine_args = create_async_engine_args(
        model=model,
        dtype=dtype,
        enforce_eager=enforce_eager,
        gpu_memory_utilization=gpu_memory_utilization,
        load_format=load_format,
        model_loader_extra_config=model_loader_extra_config,
        **kwargs
    )
    
    return create_stageable_engine_from_args(engine_args)

def get_gpu_memory_usage(device_id: int = 0) -> Dict[str, float]:
    """获取GPU显存使用情况（MB）"""
    usage = {"allocated_mb": 0.0, "reserved_mb": 0.0}
    
    if not torch.cuda.is_available() or not torch.cuda.is_initialized():
        return usage
    
    try:
        usage["allocated_mb"] = round(torch.cuda.memory_allocated(device_id) / 1024**2, 2)
        usage["reserved_mb"] = round(torch.cuda.memory_reserved(device_id) / 1024**2, 2)
    except Exception:
        pass
    
    return usage