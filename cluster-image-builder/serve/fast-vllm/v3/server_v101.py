# server_v101.py (关键修改版)
import os
import asyncio
from enum import Enum
from fastapi import FastAPI, Header, HTTPException, Request
from starlette.responses import StreamingResponse, JSONResponse
import uvicorn

import patch_v1_async_llm  # noqa: F401

from vllm.config import ModelConfig
from vllm.engine.protocol import EngineClient
from vllm.engine.arg_utils import AsyncEngineArgs
from vllm.engine.async_llm_engine import AsyncLLMEngine

# === 新增：导入 OpenAI 协议类型 和 枚举 ===
from vllm.entrypoints.openai.protocol import (
    ChatCompletionRequest,
    ChatCompletionResponse,
    ErrorResponse,
)

from vllm.entrypoints.openai.serving_chat import OpenAIServingChat
from vllm.entrypoints.openai.serving_models import (
    OpenAIServingModels,
    BaseModelPath,
)

from vllm.model_executor.model_loader.tensorizer import (
    TensorizerConfig,
)

ADMIN_TOKEN = os.getenv("ADMIN_TOKEN", "supersecret")
def ensure_admin(x_admin_token: str | None):
    if x_admin_token != ADMIN_TOKEN:
        raise HTTPException(status_code=401, detail="unauthorized")

class EngineState(str, Enum):
    INIT = "init"
    READY = "ready"
    ERROR = "error"

engine_state: EngineState = EngineState.INIT
engine_error: str | None = None
state_lock = asyncio.Lock()

os.environ.setdefault("VLLM_USE_V1", "1")
MODEL_NAME = os.getenv("VLLM_MODEL", "Qwen/Qwen3-32B")
SERVED_NAME = os.getenv("SERVED_MODEL_NAME", MODEL_NAME)

engine_args = AsyncEngineArgs(
    task="generate",
    model=SERVED_NAME,
    disable_log_stats=False,
    enable_prefix_caching=True,
    enforce_eager=True,
    tensor_parallel_size=int(os.getenv("TP_SIZE", "2")),
    max_model_len=int(os.getenv("MAX_LEN", "1024")),
    load_format="tensorizer",
    model_loader_extra_config=TensorizerConfig(
      tensorizer_uri="/home/ec2-user/image-builder-test/output/vllm/Qwen/Qwen3-32B/v1/model-rank-%03d.tensors",
    ),
)

engine_client: EngineClient | None = AsyncLLMEngine.from_engine_args(engine_args)
models_handler: OpenAIServingModels | None = None  # ← 改名，强调它是 handler 不是 router
chat_handler: OpenAIServingChat | None = None

app = FastAPI()

@app.get("/internal/engine/status")
async def status(x_admin_token: str | None = Header(default=None)):
    ensure_admin(x_admin_token)
    return {"state": engine_state, "error": engine_error, "model": MODEL_NAME}

@app.post("/internal/engine/activate")
async def activate(
    x_admin_token: str | None = Header(default=None),
    cuda_visible_devices: str | None = None,
):
    ensure_admin(x_admin_token)
    global engine_state, engine_error, engine_client, models_handler, chat_handler

    async with state_lock:
        if engine_state == EngineState.READY:
            return {"state": engine_state, "message": "already activated"}

        try:
            engine_state = EngineState.INIT
            engine_error = None
            if cuda_visible_devices is not None:
                os.environ["CUDA_VISIBLE_DEVICES"] = cuda_visible_devices

            print("start v1_vllm_engine_activate")
            engine_client.v1_async_llm_activate()
            print("done v1_vllm_engine_activate")

            model_config: ModelConfig = await engine_client.get_model_config()

            # 实例化两个 handler（注意：不是 router）
            models_handler = OpenAIServingModels(
                engine_client=engine_client,
                model_config=model_config,
                base_model_paths=[BaseModelPath(name=SERVED_NAME, model_path=SERVED_NAME)],
            )
            chat_handler = OpenAIServingChat(
                engine_client=engine_client,
                model_config=model_config,
                models=models_handler,
                response_role="assistant",
                request_logger=None,
                chat_template=None,
                chat_template_content_format="auto",
                return_tokens_as_token_ids=False,
                reasoning_parser="",
                enable_auto_tools=False,
                exclude_tools_when_tool_choice_none=False,
                tool_parser=None,
                enable_prompt_tokens_details=False,
                enable_force_include_usage=False,
                enable_log_outputs=False,
            )

            engine_state = EngineState.READY
            return {"state": engine_state, "served_model": SERVED_NAME}
        except Exception as e:
            engine_state = EngineState.ERROR
            engine_error = repr(e)
            raise HTTPException(status_code=500, detail=f"activate failed: {e}")

# === 自己挂最小化接口 ===

@app.get("/v1/models")
async def list_models():
    if models_handler is None:
        return JSONResponse({"error": "engine not ready"}, status_code=503)
    result = await models_handler.show_available_models()
    # pydantic 模型 → dict
    return JSONResponse(content=result.model_dump())

@app.post("/v1/chat/completions")
async def chat_completions(request: Request):
    if chat_handler is None:
        return JSONResponse({"error": "engine not ready"}, status_code=503)

    payload = await request.json()
    req = ChatCompletionRequest(**payload)

    # vLLM 的 handler 会返回：流生成器 / 非流 JSON / ErrorResponse
    ret = await chat_handler.create_chat_completion(req, request)

    # 流式
    if hasattr(ret, "__aiter__"):
        return StreamingResponse(ret, media_type="text/event-stream")

    # 错误
    if isinstance(ret, ErrorResponse):
        return JSONResponse(content=ret.model_dump())

    # 非流
    assert isinstance(ret, ChatCompletionResponse)
    return JSONResponse(content=ret.model_dump())

@app.get("/healthz")
def healthz():
    return {"ok": True}

if __name__ == "__main__":
    host = os.getenv("HOST", "0.0.0.0")
    port = int(os.getenv("PORT", "8000"))
    uvicorn.run(app, host=host, port=port)