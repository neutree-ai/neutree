"""vLLM v0.11.2 Backend — engine-specific inference logic.

This is a plain Python class (no ``@serve.deployment`` decorator).  The
framework's ``_Backend`` wrapper in ``app_builder.py`` handles the Ray Serve
deployment lifecycle and dynamically loads this class via ``importlib`` at
init time inside the engine container.
"""

import logging
import json
from typing import Any

from vllm.engine.arg_utils import AsyncEngineArgs
from vllm.v1.engine.async_llm import AsyncLLM
from vllm.entrypoints.openai.protocol import (
    ChatCompletionRequest, ErrorResponse,
    RerankRequest,
    EmbeddingCompletionRequest,
)
from vllm.entrypoints.openai.serving_chat import OpenAIServingChat
from vllm.entrypoints.openai.serving_embedding import OpenAIServingEmbedding
from vllm.entrypoints.openai.serving_score import ServingScores
from vllm.entrypoints.openai.serving_models import BaseModelPath, OpenAIServingModels

from downloader import get_downloader, build_request_from_model_args
from serve._metrics.ray_stat_logger import NeutreeRayStatLogger
from serve._utils import coerce_args


class Backend:
    def __init__(self,
                 model_registry_type: str,
                 model_name: str,
                 model_version: str,
                 model_file: str = "",
                 model_task: str = "",
                 model_registry_path: str = "",
                 model_path: str = "",
                 model_serve_name: str = "",
                 **engine_kwargs):
        """
        Backend for vLLM inference.

        Args:
            model_registry_type: Type of model registry ("bentoml" or "hugging-face")
            model_name: Name of the model in the registry
            model_version: Version of the model
            model_file: Specific model file name (for bentoml)
            model_task: Task type (e.g., "text-generation", "text-embedding", "text-rerank")
            **engine_kwargs: Additional keyword arguments passed directly to AsyncEngineArgs
        """
        backend, dl_req = build_request_from_model_args({
            "registry_type": model_registry_type,
            "name": model_name,
            "version": model_version,
            "file": model_file,
            "task": model_task,
            "registry_path": model_registry_path,
            "path": model_path,
        })

        downloader = get_downloader(backend)
        print(f"[Backend] Downloading model using backend={backend} from source={dl_req.source} to dest={dl_req.dest}")
        downloader.download(dl_req.source, dl_req.dest, credentials=dl_req.credentials,
                            recursive=dl_req.recursive, overwrite=dl_req.overwrite,
                            retries=dl_req.retries, timeout=dl_req.timeout, metadata=dl_req.metadata)
        print(f"[Backend] Model download completed.")

        self.model_id = model_serve_name
        self.model_task = model_task

        # Extract our custom parameters BEFORE creating AsyncEngineArgs to avoid unexpected keyword errors
        # Tool calling configuration
        self.enable_auto_tools = False
        self.tool_parser = engine_kwargs.pop("tool_call_parser", None)
        if self.tool_parser:
            self.enable_auto_tools = True

        # Reasoning configuration (read but don't pop - engine needs these too)
        self.reasoning_parser = engine_kwargs.get("reasoning_parser", None)

        # Extract chat template parameters
        self.chat_template = engine_kwargs.pop("chat_template", None)
        self.chat_template_content_format = engine_kwargs.pop("chat_template_content_format", "auto")

        # Extract other chat-specific parameters (keep defaults from vLLM)
        self.response_role = engine_kwargs.pop("response_role", "assistant")
        self.enable_prompt_tokens_details = engine_kwargs.pop("enable_prompt_tokens_details", False)

        # Map model task to vLLM task
        task = "generate"
        if model_task == "text-generation":
            task = "generate"
        elif model_task == "text-embedding":
            task = "embed"
        elif model_task in ["text-rerank", "score"]:
            task = "score"

        # merge engine args
        args = dict(
            task=task,
            model=model_path,
            served_model_name=self.model_id,
            disable_log_stats=False,
            enable_prefix_caching=True,
        )

        args.update(engine_kwargs)

        # Coerce JSON string values to native types based on AsyncEngineArgs field
        # annotations. This handles the SSH/Ray path where users may provide JSON
        # values as strings (e.g. '{"temperature": 0.5}' instead of a native dict),
        # since unlike the K8s CLI path there is no argparse layer to do json.loads.
        coerce_args(args, AsyncEngineArgs)

        engine_args = AsyncEngineArgs(
            **args
        )

        self.engine = AsyncLLM.from_engine_args(
            engine_args,
            stat_loggers=[NeutreeRayStatLogger],
        )
        self.model_config = None
        self.openai_serving_chat = None
        self.openai_serving_embedding = None
        self.openai_serving_score = None
        self.openai_serving_models = None

    def _ensure_model_config(self):
        if self.model_config is None:
            self.model_config = self.engine.model_config
        return self.model_config

    async def _ensure_models(self):
        if self.openai_serving_models is None:
            self._ensure_model_config()
            self.openai_serving_models = OpenAIServingModels(
                self.engine,
                [BaseModelPath(name=self.engine.model_config.served_model_name,
                               model_path=self.engine.model_config.served_model_name)]
            )
        return self.openai_serving_models

    async def _ensure_chat(self):
        if self.openai_serving_chat is None:
            self._ensure_model_config()
            models = await self._ensure_models()

            self.openai_serving_chat = OpenAIServingChat(
                self.engine,
                models,
                self.response_role,
                request_logger=None,
                chat_template=self.chat_template,
                chat_template_content_format=self.chat_template_content_format,
                enable_auto_tools=self.enable_auto_tools,
                tool_parser=self.tool_parser,
                reasoning_parser=self.reasoning_parser,
                enable_prompt_tokens_details=self.enable_prompt_tokens_details,
            )
        return self.openai_serving_chat

    async def _ensure_embedding(self):
        if self.openai_serving_embedding is None:
            self._ensure_model_config()
            models = await self._ensure_models()
            self.openai_serving_embedding = OpenAIServingEmbedding(
                self.engine,
                models,
                request_logger=None,
                chat_template=self.chat_template,
                chat_template_content_format=self.chat_template_content_format,
            )
        return self.openai_serving_embedding

    async def _ensure_score(self):
        if self.openai_serving_score is None:
            self._ensure_model_config()
            models = await self._ensure_models()
            self.openai_serving_score = ServingScores(
                self.engine,
                models,
                request_logger=None,
            )
        return self.openai_serving_score

    async def generate(self, payload: Any):
        await self._ensure_chat()
        result = await self.openai_serving_chat.create_chat_completion(ChatCompletionRequest(**payload), None)

        is_stream = payload.get("stream") is True

        if isinstance(result, ErrorResponse):
            if is_stream:
                logging.error(f"Error during chat completion: {result.message}")
                async def error_generator():
                    error_data = {
                        "error": {
                            "message": "Request processing failed",
                            "type": "internal_server_error",
                            "details": str(result.message)
                        }
                    }
                    yield f"data: {json.dumps(error_data)}\n\n"
                    yield "data: [DONE]\n\n"
                return error_generator()

        return result

    async def generate_embeddings(self, payload: Any):
        await self._ensure_embedding()
        try:
            request = EmbeddingCompletionRequest(**payload)
        except (TypeError, ValueError) as e:
            logging.error(f"Invalid payload for EmbeddingCompletionRequest: {e}")
            return ErrorResponse(
                message={"error": "Invalid payload for EmbeddingCompletionRequest", "details": str(e)},
                status_code=400,
            )
        return await self.openai_serving_embedding.create_embedding(request, None)

    async def rerank(self, payload: Any):
        """Rerank documents based on their relevance to a query."""
        await self._ensure_score()
        request = RerankRequest(**payload)
        return await self.openai_serving_score.do_rerank(request, None)

    async def show_available_models(self):
        models = await self._ensure_models()
        return await models.show_available_models()
