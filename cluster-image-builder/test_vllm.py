from typing import Optional
from vllm.v1.executor.abstract import Executor
from vllm.config import VllmConfig
from vllm import LLM, RequestOutput, SamplingParams
from vllm.v1.engine.llm_engine import LLMEngine as V1LLMEngine
from vllm.tasks import SupportedTask
from vllm.multimodal import MULTIMODAL_REGISTRY, MultiModalRegistry
from vllm.usage.usage_lib import UsageContext
from vllm.v1.metrics.loggers import PrometheusStatLogger, StatLoggerBase, StatLoggerFactory
import vllm.envs as envs
from vllm.transformers_utils.tokenizer_group import init_tokenizer_from_configs
from vllm.v1.engine.processor import Processor
from vllm.v1.engine.output_processor import OutputProcessor
from vllm.v1.engine.core_client import EngineCoreClient
import time
import os
from vllm.device_allocator.cumem import CuMemAllocator


def get_supported_tasks(self) -> tuple[SupportedTask, ...]:
    return ("dummy")
  
original_get_supported_tasks = V1LLMEngine.get_supported_tasks

def v1_llm_engine_init(
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
            "This should not happen. As a workaround, try using "
            "LLMEngine.from_vllm_config(...) or explicitly set "
            "VLLM_USE_V1=0 or 1 and report this issue on Github.")

    if stat_loggers is not None:
        raise NotImplementedError(
            "Passing StatLoggers to LLMEngine in V1 is not yet supported. "
            "Set VLLM_USE_V1=0 and file and issue on Github.")

    self.vllm_config = vllm_config
    self.model_config = vllm_config.model_config
    self.cache_config = vllm_config.cache_config

    self.log_stats = log_stats
    self.stat_logger: Optional[StatLoggerBase] = None
    if self.log_stats:
        self.stat_logger = PrometheusStatLogger(vllm_config)

    # important: init dp group before init the engine_core
    # In the decoupled engine case this is handled in EngineCoreProc.
    parallel_config = vllm_config.parallel_config
    if not multiprocess_mode and parallel_config.data_parallel_size > 1:
        self.dp_group = parallel_config.stateless_init_dp_group()
    else:
        self.dp_group = None
    self.should_execute_dummy_batch = False

    if self.model_config.skip_tokenizer_init:
        self.tokenizer = None
    else:
        # Tokenizer (+ ensure liveness if running in another process).
        self.tokenizer = init_tokenizer_from_configs(
            model_config=vllm_config.model_config,
            scheduler_config=vllm_config.scheduler_config,
            lora_config=vllm_config.lora_config)

    # Processor (convert Inputs --> EngineCoreRequests)
    self.processor = Processor(vllm_config=vllm_config,
                                tokenizer=self.tokenizer,
                                mm_registry=mm_registry)

    # OutputProcessor (convert EngineCoreOutputs --> RequestOutput).
    self.output_processor = OutputProcessor(self.tokenizer,
                                            log_stats=self.log_stats)

    # keep property for activate
    self.executor_class = executor_class
    self.multiprocess_mode = multiprocess_mode

def v1_vllm_engine_activate(self) -> None:
    # EngineCore (gets EngineCoreRequests and gives EngineCoreOutputs)
    self.engine_core = EngineCoreClient.make_client(
        multiprocess_mode=self.multiprocess_mode,
        asyncio_mode=False,
        vllm_config=self.vllm_config,
        executor_class=self.executor_class,
        log_stats=self.log_stats,
    )

    if not self.multiprocess_mode:
        # for v0 compatibility
        self.model_executor = self.engine_core.engine_core.model_executor  # type: ignore

    # Don't keep the dummy data in memory
    self.reset_mm_cache()
    
    V1LLMEngine.get_supported_tasks = original_get_supported_tasks

V1LLMEngine.get_supported_tasks = get_supported_tasks
V1LLMEngine.__init__ = v1_llm_engine_init
V1LLMEngine.v1_vllm_engine_activate = v1_vllm_engine_activate

# Sample prompts.
prompts = [
    "Hello, my name is",
    "The president of the United States is",
    "The capital of France is",
    "The future of AI is",
]
# Create a sampling params object.
sampling_params = SamplingParams(temperature=0.8, top_p=0.95)

def print_prompts_and_outputs(outputs: list[RequestOutput]) -> None:
    print("-" * 60)
    for output in outputs:
        prompt = output.prompt
        generated_text = output.outputs[0].text
        print(f"Prompt:    {prompt!r}")
        print(f"Output:    {generated_text!r}")
        print("-" * 60)

def main():
    os.environ["VLLM_ENABLE_V1_MULTIPROCESSING"] = "0"
    os.environ["CUDA_VISIBLE_DEVICES"] = ""
    # Create an LLM without loading real weights
    llm = LLM(
        model="Qwen/Qwen3-0.6B",
        # load_format="auto",
        # enforce_eager=True,
        # tensor_parallel_size=1,
        enable_sleep_mode=True,
    )
    print("llm initialized")
    
    time.sleep(2)
    
    os.environ["CUDA_VISIBLE_DEVICES"] = "0"

    llm.llm_engine.v1_vllm_engine_activate()
    llm.supported_tasks = llm.llm_engine.get_supported_tasks()
    print("Supported_tasks: ", llm.supported_tasks)
    outputs = llm.generate(prompts, sampling_params)
    print_prompts_and_outputs(outputs)
    
    time.sleep(5)
    
    allocator = CuMemAllocator.get_instance()
    print(f"Tracked allocations: {len(allocator.pointer_to_data)}")

    # llm.sleep(level=1)
    llm.reset_prefix_cache()
    start = time.perf_counter()
    allocator.sleep()
    duration = time.perf_counter() - start
    print(f"CuMemAllocator.sleep duration: {duration:.4f} s")

    time.sleep(10000)


if __name__ == "__main__":
    main()
