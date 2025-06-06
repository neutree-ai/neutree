import asyncio
import enum
import json
import logging
import os
from pathlib import Path
import subprocess
import time
from typing import Any, AsyncGenerator, Dict, List, Optional
from typing import  Mapping

import bentoml
from fastapi import FastAPI, Request
from fastapi.middleware import Middleware
from fastapi.middleware.cors import CORSMiddleware
from huggingface_hub import snapshot_download
from openai import OpenAI
import ray
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator
from starlette.responses import JSONResponse, StreamingResponse
from starlette_context.middleware import RawContextMiddleware
from starlette_context.plugins import RequestIdPlugin

from vllm.engine.protocol import EngineClient
from vllm.inputs.data import PromptType, TokensPrompt
from vllm.sampling_params import BeamSearchParams, SamplingParams
from vllm.lora.request import LoRARequest
from vllm.model_executor.layers.sampler import SamplerOutput
from vllm.outputs import CompletionOutput, PoolingRequestOutput, RequestOutput
from vllm.pooling_params import PoolingParams
from vllm.prompt_adapter.request import PromptAdapterRequest
from vllm.sampling_params import BeamSearchParams, SamplingParams
from vllm.transformers_utils.tokenizer import AnyTokenizer
from vllm.utils import Device, collect_from_async_generator, random_uuid
from vllm.config import DecodingConfig, ModelConfig, VllmConfig
from vllm.inputs.preprocess import InputPreprocessor
from vllm.core.scheduler import SchedulerOutputs
from serve.mindie.v1.engine.engine import Engine


class MindLEEngineClient(EngineClient):
    def __init__(self):
        # 初始化客户端状态
        self._is_running = False
        self._is_stopped = True
        self._errored = False
        self._dead_error = None
        self.engine = Engine()
        self.engine.init()

    @property
    def is_running(self) -> bool:
        return self._is_running

    @property
    def is_stopped(self) -> bool:
        return self._is_stopped

    @property
    def errored(self) -> bool:
        return self._errored

    @property
    def dead_error(self) -> BaseException:
        return self._dead_error

    async def generate(self, prompt: PromptType, sampling_params: SamplingParams, request_id: str, lora_request: Optional[LoRARequest] = None, trace_headers: Optional[Mapping[str, str]] = None, prompt_adapter_request: Optional[PromptAdapterRequest] = None, priority: int = 0) -> AsyncGenerator[RequestOutput, None]:
        return super().generate(prompt, sampling_params, request_id, lora_request, trace_headers, prompt_adapter_request, priority)
        
    async def encode(self, prompt: PromptType, pooling_params: PoolingParams, request_id: str, lora_request: Optional[LoRARequest] = None, trace_headers: Optional[Mapping[str, str]] = None, priority: int = 0) -> AsyncGenerator[PoolingRequestOutput, None]:
        return super().encode(prompt, pooling_params, request_id, lora_request, trace_headers, priority)

    async def decode(self, token_ids: List[int], request_id: str, lora_request: Optional[LoRARequest] = None, trace_headers: Optional[Mapping[str, str]] = None, priority: int = 0) -> AsyncGenerator[RequestOutput, None]:
        return super().decode(token_ids, request_id, lora_request, trace_headers, priority)

    async def abort(self, request_id: str) -> None:
        return await super().abort(request_id)

    async def get_vllm_config(self) -> VllmConfig:
        return super().get_vllm_config()
    
    async def get_model_config(self) -> ModelConfig:
        return super().get_model_config()

    async def is_tracing_enabled(self) -> bool:
        return await super().is_tracing_enabled()
    async def get_tokenizer(self) -> AnyTokenizer:
        return super().get_tokenizer()
    async def get_decoding_config(self) -> DecodingConfig:
        return super().get_decoding_config()
    
    async def get_input_preprocessor(self) -> InputPreprocessor:
        return await super().get_input_preprocessor()
    
    async def get_tracing_enabled(self) -> bool:
        return await super().get_tracing_enabled()
    
    async def do_log_stats(self, scheduler_outputs: Optional[SchedulerOutputs] = None, model_output: Optional[List[SamplerOutput]] = None) -> None:
        return await super().do_log_stats(scheduler_outputs, model_output)

    async def check_health(self) -> None:
        return await super().check_health()
    
    async def start_profile(self) -> None:
        return await super().start_profile()

    async def stop_profile(self) -> None:
        return await super().stop_profile()
    
    async def reset_prefix_cache(self) -> None:
        return await super().reset_prefix_cache()
    
    async def reset_mmuf_cache(self) -> None:
        return await super().reset_mmuf_cache()
    async def sleep(self, level: int = 1) -> None:
        return await super().sleep(level)
    
    async def wake_up(self) -> None:
        return await super().wakeup()

    async def is_sleeping(self) -> bool:
        return await super().is_sleeping()

    async def add_lora(self, lora_request: LoRARequest) -> None:
        return await super().add_lora(lora_request)

MindLEEngine()