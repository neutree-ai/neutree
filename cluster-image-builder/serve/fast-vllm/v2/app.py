"""
重构版本 - 基于精准可控的分阶段 Engine
使用动态池化策略：稳定状态1个实例，cooldown时短暂1+1，然后回归1个
实现零停机时间和彻底资源回收
"""

import ray
from ray import serve
import asyncio
import enum
import time
import os
from typing import Dict, List, Optional, Any
from dataclasses import dataclass

# 导入我们的分阶段引擎
from .stageable_engine import (
    StageableLLMEngine,
    EngineStage,
    EngineMetrics,
    EngineRecycler,
    create_stageable_engine,
    create_async_engine_args,
    create_stageable_engine_from_args,
    get_gpu_memory_usage
)

# 静态导入所有依赖
from vllm.entrypoints.openai.serving_chat import OpenAIServingChat
from vllm.entrypoints.openai.serving_embedding import OpenAIServingEmbedding
from vllm.entrypoints.openai.serving_score import ServingScores
from vllm.entrypoints.openai.serving_models import OpenAIServingModels, BaseModelPath
from vllm.entrypoints.openai.protocol import (
    ChatCompletionRequest, 
    EmbeddingRequest, 
    RerankRequest, 
    ErrorResponse
)
import torch

# FastAPI 相关导入
from fastapi import FastAPI, Request
from starlette.responses import StreamingResponse, JSONResponse
from fastapi.middleware.cors import CORSMiddleware
from starlette_context.plugins import RequestIdPlugin
from starlette_context.middleware import RawContextMiddleware

# ============================================================================
# 1. 实例状态管理（基于分阶段 Engine）
# ============================================================================

@dataclass
class EngineInstance:
    """Engine 实例 - 核心数据结构"""
    instance_id: str
    engine: StageableLLMEngine
    serving_components: Optional[Dict] = None
    
    # GPU 相关
    gpu_id: Optional[int] = None
    
    # 请求跟踪
    active_requests: int = 0
    
    # Cooldown 跟踪
    _cooldown_start_time: Optional[float] = None
    
    @property
    def stage(self) -> EngineStage:
        return self.engine.stage
    
    @property
    def metrics(self) -> EngineMetrics:
        return self.engine.metrics
    
    def can_accept_requests(self) -> bool:
        return self.engine.can_accept_requests()
    
    def is_active(self) -> bool:
        return self.engine.is_active()
    
    def is_ready_for_activation(self) -> bool:
        return self.engine.is_ready_for_activation()
    
    def has_active_requests(self) -> bool:
        return self.active_requests > 0

# ============================================================================
# 2. 智能池管理器 - 基于分阶段 Engine 的 1+1 策略
# ============================================================================

@serve.deployment(
    ray_actor_options={
        "num_cpus": 1,      # 池管理需要一些CPU
        "num_gpus": 0       # Pool本身不占GPU
    }
)
class StageablePoolManager:
    """
    基于分阶段 Engine 的智能池管理器
    
    核心策略：
    1. 稳定状态：维护1个 ACTIVE 实例
    2. Cooldown 时：启动新 Stage1，短暂1+1状态
    3. 新实例就绪后：回收旧实例，回归1个实例
    4. COOLDOWN 实例仍可接受请求，保证零停机时间
    5. 直接处理HTTP请求，无需中间层
    """
    
    def __init__(self, model_configs: List[Dict], pool_config: Dict):
        # 支持多个模型配置
        self.model_configs = model_configs if isinstance(model_configs, list) else [model_configs]
        self.current_model_config = self.model_configs[0]  # 默认使用第一个配置
        
        # 预构建所有模型的 AsyncEngineArgs
        self.prepared_engine_args = {}
        for model_config in self.model_configs:
            engine_args = self._create_async_engine_args(model_config)
            model_name = model_config.get('served_model_name')
            self.prepared_engine_args[model_name] = engine_args
            print(f"[POOL_MANAGER] Prepared engine args for model: {model_name}")
        
        self.total_gpus = pool_config.get('total_gpus', 1)
        self.gpu_per_instance = pool_config.get('gpu_per_instance', 1)
        # 使用传入的 cooldown_delay，没有默认值则使用 60 秒
        self.cooldown_delay = pool_config.get('cooldown_delay', 60)  # 空闲多久后触发cooldown  
        self.recycle_delay = pool_config.get('recycle_delay', 30)   # cooldown后多久彻底回收
        
        # GPU分配跟踪
        self.gpu_allocation: Dict[int, Optional[str]] = {
            i: None for i in range(self.total_gpus)
        }
        
        # 实例管理 - 简化为单一字典
        self.instances: Dict[str, EngineInstance] = {}
        self.instance_counter = 0
        
        print(f"StageablePoolManager: total_gpus={self.total_gpus}, cooldown_delay={self.cooldown_delay}s")
        
        # 启动后台任务
        asyncio.create_task(self._initialize_first_instance())
        asyncio.create_task(self._maintain_pool())
        asyncio.create_task(self._monitor_lifecycle())
    
    async def _initialize_first_instance(self):
        """初始化第一个实例 - 直接创建为 ACTIVE 状态"""
        print("Initializing first instance...")
        
        try:
            # 创建第一个实例
            instance_id = await self._create_instance(target_stage=EngineStage.STAGE2_ACTIVE)
            
            if instance_id:
                print(f"✅ First instance {instance_id} initialized successfully")
            else:
                print("❌ Failed to initialize first instance")
                
        except Exception as e:
            print(f"First instance initialization error: {e}")
            import traceback
            print(f"Traceback: {traceback.format_exc()}")
    
    async def _create_instance(self, target_stage: EngineStage, allow_gpu_sharing: bool = False) -> Optional[str]:
        """创建新实例到指定阶段"""
        self.instance_counter += 1
        instance_id = f"engine_{self.instance_counter:03d}"
        
        sharing_msg = " (GPU sharing allowed)" if allow_gpu_sharing else ""
        print(f"Creating instance {instance_id} to stage {target_stage.value}{sharing_msg}...")
        start_time = time.time()
        gpu_id = None
        
        try:
            # 分配 GPU
            gpu_id = self._allocate_gpu(allow_shared=allow_gpu_sharing)
            if gpu_id is None:
                error_msg = "No available GPU for new instance"
                if allow_gpu_sharing:
                    error_msg += " (even with sharing)"
                print(error_msg)
                return None
            
            # 设置 GPU 环境
            os.environ['CUDA_VISIBLE_DEVICES'] = str(gpu_id)
            # print(f"[STAGEABLE_ENGINE] Set CUDA_VISIBLE_DEVICES to: {gpu_id}")

            # 使用预构建的 engine args 创建分阶段引擎
            model_name = self.current_model_config.get('served_model_name')
            if model_name in self.prepared_engine_args:
                engine_args = self.prepared_engine_args[model_name]
                engine = create_stageable_engine_from_args(engine_args)
            else:
                # 如果没有预构建的，就现场构建
                engine_args = self._create_async_engine_args(self.current_model_config)
                engine = create_stageable_engine_from_args(engine_args)
            
            # 执行 Stage 1
            if not engine.initialize_stage1():
                # Stage1 失败时，只有非共享模式才释放 GPU
                if not allow_gpu_sharing:
                    self._release_gpu(gpu_id)
                return None
            
            # 根据目标阶段决定是否继续
            if target_stage == EngineStage.STAGE2_ACTIVE:
                if not engine.initialize_stage2():
                    # Stage2 失败时，只有非共享模式才释放 GPU
                    if not allow_gpu_sharing:
                        self._release_gpu(gpu_id)
                    return None
                
                # 创建 serving 组件
                serving_components = await self._create_serving_components(engine)
            else:
                serving_components = None
            
            # 创建实例对象
            instance = EngineInstance(
                instance_id=instance_id,
                engine=engine,
                serving_components=serving_components,
                gpu_id=gpu_id
            )
            
            self.instances[instance_id] = instance
            
            # 只有非共享模式才更新 gpu_allocation
            if not allow_gpu_sharing:
                self.gpu_allocation[gpu_id] = instance_id
            
            creation_time = time.time() - start_time
            stage_msg = f"stage: {engine.stage.value}"
            if allow_gpu_sharing:
                stage_msg += f", shared GPU: {gpu_id}"
            print(f"✅ Instance {instance_id} created in {creation_time:.2f}s ({stage_msg})")
            
            return instance_id
            
        except Exception as e:
            print(f"❌ Failed to create instance {instance_id}: {e}")
            # 失败时，只有非共享模式才释放 GPU
            if gpu_id is not None and not allow_gpu_sharing:
                self._release_gpu(gpu_id)
            return None
    
    async def _create_instance_with_shared_gpu(self, target_stage: EngineStage) -> Optional[str]:
        """创建新实例到指定阶段，允许与 cooldown 实例共享 GPU"""
        return await self._create_instance(target_stage, allow_gpu_sharing=True)
    
    async def _maintain_pool(self):
        """维护池状态 - 确保稳定的单实例，cooldown时短暂1+1"""
        while True:
            try:
                # 统计各阶段实例
                active_instances = [
                    inst for inst in self.instances.values()
                    if inst.stage == EngineStage.STAGE2_ACTIVE
                ]
                
                stage1_ready_instances = [
                    inst for inst in self.instances.values()
                    if inst.stage == EngineStage.STAGE1_READY
                ]
                
                cooldown_instances = [
                    inst for inst in self.instances.values()
                    if inst.stage == EngineStage.STAGE2_COOLDOWN
                ]
                
                await asyncio.sleep(5)  # 每5秒检查一次
                
            except Exception as e:
                print(f"Pool maintenance error: {e}")
                await asyncio.sleep(10)
    
    async def _activate_instance(self, instance_id: str, model_name: str = None) -> bool:
        """激活 Stage1 实例到 Stage2"""
        if instance_id not in self.instances:
            return False
        
        instance = self.instances[instance_id]
        
        if not instance.is_ready_for_activation():
            print(f"Instance {instance_id} is not ready for activation (stage: {instance.stage.value})")
            return False
        
        print(f"Activating instance {instance_id} to Stage2...")
        start_time = time.time()
        
        try:
            # 从API payload读取model name，找到对应的args
            if not model_name or model_name not in self.prepared_engine_args:
                print(f"Unknown or missing model name: {model_name}")
                return False
            
            ea = self.prepared_engine_args[model_name]
            # 执行 Stage2 初始化
            if not instance.engine.initialize_stage2(ea):
                return False
            
            # 创建 serving 组件
            instance.serving_components = await self._create_serving_components(instance.engine)
            
            activation_time = time.time() - start_time
            print(f"✅ Instance {instance_id} activated in {activation_time:.2f}s")
            
            return True
            
        except Exception as e:
            print(f"❌ Failed to activate instance {instance_id}: {e}")
            return False
    
    async def _monitor_lifecycle(self):
        """监控实例生命周期 - cooldown 和回收"""
        while True:
            try:
                current_time = time.time()
                
                for instance_id, instance in list(self.instances.items()):
                    # 检查是否需要触发 cooldown
                    if instance.stage == EngineStage.STAGE2_ACTIVE:
                        if self._should_trigger_cooldown(instance, current_time):
                            print(f"Triggering cooldown for instance {instance_id}")
                            await self._trigger_cooldown(instance_id)
                    
                    # 检查是否需要彻底回收
                    elif instance.stage == EngineStage.STAGE2_COOLDOWN:
                        if self._should_recycle(instance, current_time):
                            print(f"Recycling instance {instance_id}")
                            await self._recycle_instance(instance_id)
                            
                            print(f"Starting new Stage1 instance while {instance_id} is in cooldown (entering 1+1 state)")
                            await self._create_instance_with_shared_gpu(target_stage=EngineStage.STAGE1_READY)
                
                await asyncio.sleep(2)  # 每2秒检查一次
                
            except Exception as e:
                print(f"Lifecycle monitor error: {e}")
                await asyncio.sleep(10)
    
    def _should_trigger_cooldown(self, instance: EngineInstance, current_time: float) -> bool:
        """检查是否应该触发 cooldown"""
        # 如果有活跃请求，不 cooldown
        if instance.has_active_requests():
            return False
        
        # 检查空闲时间
        last_activity = instance.metrics.last_request_at or instance.metrics.stage2_completed_at or current_time
        idle_time = current_time - last_activity

        if idle_time > self.cooldown_delay:
            # Format timestamps into readable strings and handle None values
            stage2_ts = instance.metrics.stage2_completed_at
            stage2_str = time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(stage2_ts)) if stage2_ts else 'N/A'
            last_activity_str = time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(last_activity)) if last_activity else 'N/A'
            current_time_str = time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(current_time))
            print(f"Going to trigger cooldown for instance, stage2 completed at: {stage2_str}, last activity at: {last_activity_str}, current time: {current_time_str}")
            return True
          
        return False

    def _should_recycle(self, instance: EngineInstance, current_time: float) -> bool:
        """检查是否应该彻底回收"""
        # 如果有活跃请求，等待
        if instance.has_active_requests():
            return False
        
        # TODO: ?
        # 检查是否有新的预备实例
        # stage1_ready_count = len([
        #     inst for inst in self.instances.values()
        #     if inst.stage == EngineStage.STAGE1_READY
        # ])
        
        # # 只有在有预备实例时才回收
        # if stage1_ready_count == 0:
        #     return False
        
        # 检查 cooldown 时间
        cooldown_start = getattr(instance, '_cooldown_start_time', current_time)
        cooldown_duration = current_time - cooldown_start
        
        return cooldown_duration > self.recycle_delay
    
    async def _trigger_cooldown(self, instance_id: str) -> bool:
        """触发实例 cooldown - 标记为 cooldown 但仍可服务，同时启动新的 Stage1"""
        if instance_id not in self.instances:
            return False
        
        instance = self.instances[instance_id]
        
        if instance.stage != EngineStage.STAGE2_ACTIVE:
            return False
        
        try:
            # 标记为 cooldown
            instance.engine.mark_cooldown()
            instance._cooldown_start_time = time.time()
            
            # 此时开始短暂的1+1状态：启动新的 Stage1 实例（可以共享 GPU）
            # TODO: check
            # print(f"Starting new Stage1 instance while {instance_id} is in cooldown (entering 1+1 state)")
            # await self._create_instance_with_shared_gpu(target_stage=EngineStage.STAGE1_READY)
            
            return True
            
        except Exception as e:
            print(f"Failed to trigger cooldown for {instance_id}: {e}")
            return False
    
    async def _recycle_instance(self, instance_id: str) -> bool:
        """彻底回收实例资源"""
        if instance_id not in self.instances:
            return False
        
        instance = self.instances[instance_id]
        
        try:
            print(f"Starting recycling process for {instance_id}")
            
            # 首先关闭 serving 组件中的引擎包装器
            if instance.serving_components:
                for component_name, component in instance.serving_components.items():
                    if hasattr(component, 'engine'):
                        engine_obj = getattr(component, 'engine')
                        if hasattr(engine_obj, 'shutdown'):
                            print(f"Shutting down engine in {component_name}")
                            try:
                                shutdown_method = getattr(engine_obj, 'shutdown')
                                import inspect
                                if inspect.iscoroutinefunction(shutdown_method):
                                    await shutdown_method()
                                else:
                                    shutdown_method()
                            except Exception as e:
                                print(f"Error shutting down engine in {component_name}: {e}")
            
            # 然后使用 EngineRecycler 进行彻底回收
            success = await EngineRecycler.recycle_engine(instance.engine, force=False)
            del instance.engine
            del instance.serving_components
            if torch.cuda.is_available():
                torch.cuda.empty_cache()
                
            print(f"[RECYCLER] Engine recycling completed successfully")
            
            if success:
                # 检查是否有其他实例共享同一个 GPU
                sharing_instances = [
                    inst for inst in self.instances.values()
                    if inst.gpu_id == instance.gpu_id and inst.instance_id != instance_id
                ]
                
                # 只有当没有其他实例共享时才释放 GPU
                if not sharing_instances and instance.gpu_id is not None:
                    self._release_gpu(instance.gpu_id)
                    print(f"Released GPU {instance.gpu_id} (no sharing instances)")
                elif sharing_instances:
                    # 将 GPU 分配给剩余的共享实例
                    remaining_instance = sharing_instances[0]
                    self.gpu_allocation[instance.gpu_id] = remaining_instance.instance_id
                    print(f"GPU {instance.gpu_id} transferred to instance {remaining_instance.instance_id}")
                
                # 从实例池中移除
                del self.instances[instance_id]
                del instance
                
                print(f"✅ Instance {instance_id} recycled successfully")
                return True
            else:
                print(f"❌ Failed to recycle instance {instance_id}")
                return False
                
        except Exception as e:
            print(f"❌ Error recycling instance {instance_id}: {e}")
            return False
    
    async def _create_serving_components(self, engine: StageableLLMEngine) -> Dict:
        """为 engine 创建 serving 组件"""
        try:
            # 获取模型配置
            model_config = engine.vllm_config.model_config
            
            # 创建 AsyncEngine 包装器（具有 errored 属性）
            async_engine_wrapper = engine.create_async_engine()
            
            # 创建基础 serving models
            serving_models = OpenAIServingModels(
                async_engine_wrapper,  # 使用包装器
                model_config,
                [BaseModelPath(
                    name=model_config.served_model_name,
                    model_path=model_config.served_model_name
                )],
                lora_modules=None,
                prompt_adapters=None,
            )
            
            # 根据模型任务创建对应组件
            serving_components = {'models': serving_models}
            model_task = self.current_model_config.get('task', 'text-generation')
            
            if model_task in ['text-generation', 'chat']:
                serving_components['chat'] = OpenAIServingChat(
                    async_engine_wrapper,  # 使用包装器
                    model_config,
                    serving_models,
                    response_role=self.current_model_config.get('response_role', 'assistant'),
                    request_logger=None,
                    chat_template=self.current_model_config.get('chat_template'),
                    chat_template_content_format=self.current_model_config.get('chat_template_content_format', 'auto'),
                    enable_auto_tools=self.current_model_config.get('enable_auto_tools', False),
                    tool_parser=self.current_model_config.get('tool_parser'),
                    enable_reasoning=self.current_model_config.get('enable_reasoning', False),
                    reasoning_parser=self.current_model_config.get('reasoning_parser'),
                    enable_prompt_tokens_details=self.current_model_config.get('enable_prompt_tokens_details', False),
                )
            
            if model_task == 'text-embedding':
                serving_components['embedding'] = OpenAIServingEmbedding(
                    async_engine_wrapper,  # 使用包装器
                    model_config,
                    serving_models,
                    request_logger=None,
                    chat_template=self.current_model_config.get('chat_template'),
                    chat_template_content_format=self.current_model_config.get('chat_template_content_format', 'auto'),
                )
            
            if model_task in ['text-rerank', 'score']:
                serving_components['score'] = ServingScores(
                    async_engine_wrapper,  # 使用包装器
                    model_config,
                    serving_models,
                    request_logger=None,
                )
            
            return serving_components
            
        except Exception as e:
            print(f"❌ Failed to create serving components: {e}")
            return {}
    
    def _create_async_engine_args(self, model_config: Dict):
        """为指定的模型配置创建 AsyncEngineArgs"""
        engine_kwargs = model_config.get('engine_kwargs', {}).copy()
        tensorizer_config = _create_tensorizer_config(model_config)
        print(f"[STAGEABLE_ENGINE] Creating AsyncEngineArgs with: {engine_kwargs}")

        return create_async_engine_args(
            model=model_config.get('served_model_name'),
            enforce_eager=True,
            gpu_memory_utilization=model_config.get('gpu_memory_utilization', 0.9),
            load_format="tensorizer",
            model_loader_extra_config=tensorizer_config,
            **engine_kwargs
        )
    
    def _allocate_gpu(self, allow_shared: bool = False) -> Optional[int]:
        """分配可用GPU"""
        # 首先尝试找到完全空闲的GPU
        for gpu_id, allocated_instance in self.gpu_allocation.items():
            if allocated_instance is None:
                return gpu_id
        
        # 如果允许共享，查找有cooldown实例的GPU（可以共享）
        if allow_shared:
            for gpu_id, allocated_instance_id in self.gpu_allocation.items():
                if allocated_instance_id and allocated_instance_id in self.instances:
                    instance = self.instances[allocated_instance_id]
                    if instance.stage == EngineStage.STAGE2_COOLDOWN:
                        print(f"Sharing GPU {gpu_id} with cooldown instance {allocated_instance_id}")
                        return gpu_id
        
        return None
    
    def _release_gpu(self, gpu_id: int):
        """释放GPU"""
        if gpu_id is not None:
            self.gpu_allocation[gpu_id] = None
    
    # ============================================================================
    # Backend功能 - 直接处理各种请求
    # ============================================================================
    
    async def generate(self, payload: Any):
        """处理chat生成请求"""
        instance = None
        
        try:
            print(f"Processing generate request, payload type: {type(payload)}")
            
            # 确保payload是字典格式
            if not isinstance(payload, dict):
                return ErrorResponse(
                    message="Invalid payload format",
                    type="invalid_request_error",
                    code=400
                )
            
            is_stream = payload.get("stream", False)
            model_name = payload.get("model")
            print(f"Stream mode: {is_stream}, Model: {model_name}")
            
            # 获取或激活实例
            instance = await self._get_available_instance(model_name)
            if instance is None:
                return ErrorResponse(
                    message="No available instance",
                    type="service_unavailable",
                    code=503
                )
            
            # 更新请求统计
            instance.active_requests += 1
            instance.metrics.total_requests += 1
            instance.metrics.last_request_at = time.time()
            
            # 检查chat组件是否可用
            if 'chat' not in instance.serving_components:
                return ErrorResponse(
                    message="Chat serving not available",
                    type="service_unavailable",
                    code=503
                )
            
            # 创建请求对象
            try:
                chat_request = ChatCompletionRequest(**payload)
            except Exception as e:
                return ErrorResponse(
                    message=f"Invalid chat request: {str(e)}",
                    type="invalid_request_error",
                    code=400
                )
            
            # 调用chat completion
            print(f"Calling create_chat_completion, stream={is_stream}")
            result = await instance.serving_components['chat'].create_chat_completion(
                chat_request, 
                raw_request=None
            )
            
            # 推理结束后刷新时间
            instance.metrics.last_request_at = time.time()
            
            print(f"Got result type: {type(result)}")
            return result
                
        except Exception as e:
            print(f"Generate error: {e}")
            import traceback
            print(f"Traceback: {traceback.format_exc()}")
            
            return ErrorResponse(
                message=str(e),
                type="internal_server_error",
                code=500
            )
        
        finally:
            # 减少处理中的请求计数
            if instance is not None:
                instance.active_requests = max(0, instance.active_requests - 1)
                print(f"Request completed, remaining active requests: {instance.active_requests}")
    
    async def generate_embeddings(self, payload: Any):
        """处理embedding请求"""
        instance = None
        
        try:
            if not isinstance(payload, dict):
                return ErrorResponse(
                    message="Invalid payload format",
                    type="invalid_request_error",
                    code=400
                )
            
            model_name = payload.get("model")
            instance = await self._get_available_instance(model_name)
            if instance is None:
                return ErrorResponse(
                    message="No available instance",
                    type="service_unavailable",
                    code=503
                )
            
            # 更新请求统计
            instance.active_requests += 1
            instance.metrics.total_requests += 1
            instance.metrics.last_request_at = time.time()
            
            if 'embedding' not in instance.serving_components:
                return ErrorResponse(
                    message="Embedding serving not available",
                    type="service_unavailable",
                    code=503
                )
            
            try:
                embedding_request = EmbeddingRequest(**payload)
            except Exception as e:
                return ErrorResponse(
                    message=f"Invalid embedding request: {str(e)}",
                    type="invalid_request_error",
                    code=400
                )
            
            result = await instance.serving_components['embedding'].create_embedding(
                embedding_request, 
                raw_request=None
            )
            
            instance.metrics.last_request_at = time.time()
            
            return result
                
        except Exception as e:
            print(f"Embedding error: {e}")
            return ErrorResponse(
                message=str(e),
                type="internal_server_error",
                code=500
            )
        
        finally:
            if instance is not None:
                instance.active_requests = max(0, instance.active_requests - 1)
    
    async def rerank(self, payload: Any):
        """处理rerank请求"""
        instance = None
        
        try:
            if not isinstance(payload, dict):
                return ErrorResponse(
                    message="Invalid payload format",
                    type="invalid_request_error",
                    code=400
                )
            
            model_name = payload.get("model")
            instance = await self._get_available_instance(model_name)
            if instance is None:
                return ErrorResponse(
                    message="No available instance",
                    type="service_unavailable",
                    code=503
                )
            
            # 更新请求统计
            instance.active_requests += 1
            instance.metrics.total_requests += 1
            instance.metrics.last_request_at = time.time()
            
            if 'score' not in instance.serving_components:
                return ErrorResponse(
                    message="Score serving not available",
                    type="service_unavailable",
                    code=503
                )
            
            try:
                rerank_request = RerankRequest(**payload)
            except Exception as e:
                return ErrorResponse(
                    message=f"Invalid rerank request: {str(e)}",
                    type="invalid_request_error",
                    code=400
                )
            
            result = await instance.serving_components['score'].do_rerank(
                rerank_request, 
                raw_request=None
            )
            
            instance.metrics.last_request_at = time.time()
            
            return result
                
        except Exception as e:
            print(f"Rerank error: {e}")
            return ErrorResponse(
                message=str(e),
                type="internal_server_error",
                code=500
            )
        
        finally:
            if instance is not None:
                instance.active_requests = max(0, instance.active_requests - 1)
    
    async def show_available_models(self):
        """显示可用模型"""
        return {
            "object": "list",
            "data": [{
                "id": self.current_model_config.get('served_model_name', 'vllm-model'),
                "object": "model",
                "created": int(time.time()),
                "owned_by": "vllm"
            }]
        }
    
    async def _get_available_instance(self, model_name: str = None) -> Optional[EngineInstance]:
        """获取可用实例 - 如果没有则激活一个"""
        # 查找可用的活跃实例
        available_active = [
            inst for inst in self.instances.values()
            if inst.stage == EngineStage.STAGE2_ACTIVE
        ]
        
        if available_active:
            # 选择负载最少的实例
            return min(available_active, key=lambda x: x.active_requests)
        
        # 查找可用的cooldown实例
        available_cooldown = [
            inst for inst in self.instances.values()
            if inst.stage == EngineStage.STAGE2_COOLDOWN
        ]
        
        if available_cooldown:
            # 使用cooldown实例（仍可服务）
            return min(available_cooldown, key=lambda x: x.active_requests)
        
        # 没有活跃或cooldown实例，激活一个预热实例
        stage1_ready = [
            inst for inst in self.instances.values()
            if inst.stage == EngineStage.STAGE1_READY
        ]
        
        if stage1_ready:
            instance = stage1_ready[0]
            success = await self._activate_instance(instance.instance_id, model_name)
            if success:
                return instance
        
        return None
    
    async def check_health(self) -> Dict[str, Any]:
        """健康检查"""
        return {
            "status": "healthy",
            "message": "Stageable pool manager ready",
            "ready": True,
            "instances": len(self.instances)
        }
    
    async def get_stats(self) -> Dict:
        """获取池统计信息"""
        active_instances = [inst for inst in self.instances.values() if inst.stage == EngineStage.STAGE2_ACTIVE]
        stage1_ready_instances = [inst for inst in self.instances.values() if inst.stage == EngineStage.STAGE1_READY]
        cooldown_instances = [inst for inst in self.instances.values() if inst.stage == EngineStage.STAGE2_COOLDOWN]
        
        # 计算激活时间统计
        activation_times = [
            inst.metrics.stage2_time for inst in self.instances.values()
            if inst.metrics.stage2_time > 0
        ]
        avg_activation_time = sum(activation_times) / len(activation_times) if activation_times else 0
        
        return {
            'strategy': 'stageable_dynamic',
            'total_instances': len(self.instances),
            'active_instances': len(active_instances),
            'stage1_ready_instances': len(stage1_ready_instances),
            'cooldown_instances': len(cooldown_instances),
            'available_gpus': sum(1 for gpu in self.gpu_allocation.values() if gpu is None),
            'gpu_allocation': self.gpu_allocation,
            'avg_activation_time': avg_activation_time,
            'target_activation_time': 10.0,
            'cooldown_delay': self.cooldown_delay,
            'recycle_delay': self.recycle_delay,
            'instances': {
                inst.instance_id: {
                    'stage': inst.stage.value,
                    'gpu_id': inst.gpu_id,
                    'stage1_time': inst.metrics.stage1_time,
                    'stage2_time': inst.metrics.stage2_time,
                    'total_requests': inst.metrics.total_requests,
                    'active_requests': inst.active_requests,
                } for inst in self.instances.values()
            }
        }

# ============================================================================
# 3. Controller（保持与原来兼容的接口）
# ============================================================================

# 调度器枚举
class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"

# FastAPI应用实例
controller_app = FastAPI()
controller_app.add_middleware(RawContextMiddleware, plugins=(RequestIdPlugin(),))
controller_app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(controller_app)
class Controller:
    """
    Controller - 直接连接 StageablePoolManager
    """
    
    def __init__(self,
                 stageable_pool_manager: Any,
                 scheduler_type: str = SchedulerType.POW2,
                 virtual_nodes: int = 100,
                 load_factor: float = 1.25):
        
        self.stageable_pool_manager = stageable_pool_manager
        print(f"[Controller] Connected to StageablePoolManager")

    @controller_app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        """Chat completions endpoint"""
        try:
            req_obj = await request.json()
            stream = req_obj.get("stream", False)
            
            print(f"Received chat request, stream={stream}")

            if stream:
                # 流式响应处理
                from ray.serve.handle import DeploymentResponseGenerator
                r: DeploymentResponseGenerator = self.stageable_pool_manager.options(stream=True).generate.remote(req_obj)

                async def stream_generator():
                    try:
                        async for chunk in r:
                            if isinstance(chunk, str):
                                yield chunk
                            elif hasattr(chunk, 'model_dump_json'):
                                yield f"data: {chunk.model_dump_json()}\n\n"
                            else:
                                yield f"data: {str(chunk)}\n\n"
                    except Exception as e:
                        print(f"Streaming error: {e}")
                        yield f"data: {{\"error\": \"{str(e)}\"}}\n\n"
                    finally:
                        yield "data: [DONE]\n\n"

                return StreamingResponse(
                    content=stream_generator(),
                    media_type="text/event-stream",
                    headers={
                        "Cache-Control": "no-cache",
                        "Connection": "keep-alive",
                        "X-Accel-Buffering": "no"
                    }
                )
            else:
                # 非流式响应处理
                result = await self.stageable_pool_manager.options(stream=False).generate.remote(req_obj)
                
                print(f"Received result type: {type(result)}")
                
                # 处理错误响应
                if hasattr(result, 'code') and hasattr(result, 'message'):
                    return JSONResponse(
                        content={
                            "error": {
                                "message": result.message,
                                "type": getattr(result, 'type', 'unknown'),
                                "code": result.code
                            }
                        }, 
                        status_code=result.code if hasattr(result, 'code') else 500
                    )
                elif isinstance(result, dict) and result.get("error"):
                    return JSONResponse(
                        content=result, 
                        status_code=result.get("code", 500)
                    )
                
                # 正常响应
                if hasattr(result, 'model_dump'):
                    return JSONResponse(content=result.model_dump())
                else:
                    return JSONResponse(content=result)
                    
        except Exception as e:
            print(f"Chat endpoint error: {e}")
            import traceback
            print(f"Traceback: {traceback.format_exc()}")
            
            return JSONResponse(
                content={
                    "error": {
                        "message": str(e),
                        "type": "internal_server_error"
                    }
                },
                status_code=500
            )

    @controller_app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        """Embeddings endpoint"""
        try:
            req_obj = await request.json()
            result = await self.stageable_pool_manager.options(stream=False).generate_embeddings.remote(req_obj)
            
            if hasattr(result, 'code') and hasattr(result, 'message'):
                return JSONResponse(
                    content={
                        "error": {
                            "message": result.message,
                            "type": getattr(result, 'type', 'unknown')
                        }
                    }, 
                    status_code=result.code
                )
            elif isinstance(result, dict) and result.get("error"):
                return JSONResponse(content=result, status_code=result.get("code", 500))
            
            return JSONResponse(content=result.model_dump() if hasattr(result, 'model_dump') else result)
            
        except Exception as e:
            print(f"Embeddings endpoint error: {e}")
            return JSONResponse(
                content={"error": {"message": str(e), "type": "internal_server_error"}},
                status_code=500
            )

    @controller_app.post("/v1/rerank")
    async def rerank(self, request: Request):
        """Rerank endpoint"""
        try:
            req_obj = await request.json()
            result = await self.stageable_pool_manager.options(stream=False).rerank.remote(req_obj)
            
            if hasattr(result, 'code') and hasattr(result, 'message'):
                return JSONResponse(
                    content={
                        "error": {
                            "message": result.message,
                            "type": getattr(result, 'type', 'unknown')
                        }
                    }, 
                    status_code=result.code
                )
            elif isinstance(result, dict) and result.get("error"):
                return JSONResponse(content=result, status_code=result.get("code", 500))
            
            return JSONResponse(content=result.model_dump() if hasattr(result, 'model_dump') else result)
            
        except Exception as e:
            print(f"Rerank endpoint error: {e}")
            return JSONResponse(
                content={"error": {"message": str(e), "type": "internal_server_error"}},
                status_code=500
            )

    @controller_app.get("/v1/models")
    async def models(self, request: Request):
        """Available models endpoint"""
        try:
            result = await self.stageable_pool_manager.show_available_models.remote()
            return JSONResponse(content=result.model_dump() if hasattr(result, 'model_dump') else result)
        except Exception as e:
            print(f"Models endpoint error: {e}")
            return JSONResponse(
                content={"error": {"message": str(e), "type": "internal_server_error"}},
                status_code=500
            )

    @controller_app.get("/health")
    async def health(self):
        """健康检查endpoint"""
        try:
            health_status = await self.stageable_pool_manager.check_health.remote()
            
            if health_status.get("ready", False):
                return JSONResponse(content=health_status, status_code=200)
            else:
                return JSONResponse(content=health_status, status_code=503)
                
        except Exception as e:
            return JSONResponse(
                content={
                    "status": "error",
                    "message": f"Health check failed: {str(e)}",
                    "ready": False
                },
                status_code=503
            )
    
    @controller_app.get("/stats")
    async def stats(self):
        """统计信息endpoint"""
        try:
            result = await self.stageable_pool_manager.get_stats.remote()
            return JSONResponse(content=result)
        except Exception as e:
            return JSONResponse(content={"error": str(e)}, status_code=500)

# ============================================================================
# 4. 应用构建器
# ============================================================================

def app_builder(args: Dict[str, Any]) -> Any:
    """
    基于分阶段 Engine 的应用构建器
    """
    # 提取配置
    models = args.get('models', [])  # 改为 models 数组
    deployment_options = args.get('deployment_options', {})
    
    # 池配置 - 兼容 warm_pool 配置名
    pool_config = deployment_options.get('stageable_pool') or deployment_options.get('warm_pool', {
        'total_gpus': 1,
        'gpu_per_instance': 1,
        'cooldown_delay': 60,
        'recycle_delay': 30
    })
    
    # 模型配置数组
    model_configs = []
    for model in models:
        engine_args = model.get('engine_args', {})
        model_config = {
            'served_model_name': model.get('name'),
            'task': model.get('task', 'text-generation'),
            'gpu_memory_utilization': engine_args.get('gpu_memory_utilization', 0.9),
            'tensorizer_config': _create_tensorizer_config({'engine_kwargs': engine_args}),
            'engine_kwargs': engine_args,
            **{k: v for k, v in engine_args.items() if k.startswith(('chat_', 'enable_', 'response_', 'tool_', 'reasoning_'))}
        }
        model_configs.append(model_config)
    
    # 1. 创建分阶段池管理器
    stageable_pool_manager = StageablePoolManager.bind(
        model_configs=model_configs,
        pool_config=pool_config
    )
    
    # 2. 创建Controller
    controller_options = deployment_options.get('controller', {})
    scheduler_config = deployment_options.get('scheduler', {})
    
    controller = Controller.options(
        num_replicas=controller_options.get('num_replicas', 1),
        ray_actor_options={
            "num_cpus": controller_options.get('num_cpus', 0.1),
            "num_gpus": 0
        }
    ).bind(
        stageable_pool_manager=stageable_pool_manager,
        scheduler_type=scheduler_config.get('type', SchedulerType.POW2),
        virtual_nodes=scheduler_config.get('virtual_nodes', 100),
        load_factor=scheduler_config.get('load_factor', 1.25)
    )
    
    return controller

# ============================================================================
# 5. Tensorizer配置辅助函数
# ============================================================================

def _create_tensorizer_config(model_config: Dict) -> Dict:
    """创建 Tensorizer 配置"""
    # 首先尝试从 tensorizer_config 获取
    tensorizer_config_dict = model_config.get('tensorizer_config')
    
    if tensorizer_config_dict:
        return tensorizer_config_dict
    
    # 如果没有 tensorizer_config，从 engine_kwargs 中构建
    engine_kwargs = model_config.get('engine_kwargs', {})
    
    tensorizer_uri = (
        engine_kwargs.get('tensorizer_uri') or 
        engine_kwargs.get('tensorizer_path') or
        (engine_kwargs.get('model_loader_extra_config', {}) or {}).get('tensorizer_uri')
    )
    
    if not tensorizer_uri:
        print(engine_kwargs, model_config)
        raise ValueError("tensorizer_uri or tensorizer_path is required but not provided")
    
    config = {
        'tensorizer_uri': tensorizer_uri,
        'num_readers': engine_kwargs.get('num_readers', 4),
    }
    
    # 添加可选的S3配置
    for key in ['s3_endpoint', 's3_access_key_id', 's3_secret_access_key']:
        if engine_kwargs.get(key):
            config[key] = engine_kwargs[key]
    
    # 添加其他tensorizer配置
    for key, value in engine_kwargs.items():
        if key.startswith('tensorizer_') and key != 'tensorizer_uri' and value is not None:
            config[key] = value
    
    model_name = model_config.get('served_model_name', 'default')
    print(f"Built tensorizer config for model {model_name}: {config}")
    return config

