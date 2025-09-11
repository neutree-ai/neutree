#!/usr/bin/env python3
"""
优化模型加载脚本 - 流式首Token时间测试
测量从模型加载到首个token流出的时间
"""

import torch
import time
import gc
import argparse
from transformers import AutoModelForCausalLM, AutoTokenizer, TextIteratorStreamer
from threading import Thread
from vllm.device_allocator.cumem import find_loaded_library
from vllm.distributed.device_communicators.cuda_wrapper import CudaRTLibrary

lib_name = find_loaded_library("cumem_allocator")
libcudart = CudaRTLibrary()

def human_size(nbytes):
    gb = 1024**3
    return f"{nbytes/gb:.2f} GB"

def gbps(nbytes, secs):
    return (nbytes / (1024**3)) / max(secs, 1e-9)

class StreamingTokenLoader:
    def __init__(self, model_path, device="cuda:0", batch_size=20):
        self.model_path = model_path
        self.device = device
        self.batch_size = batch_size
        self.tokenizer = None
        self.model = None
        
    def load_tokenizer(self):
        """加载tokenizer"""
        print(f"=== 加载 Tokenizer ===")
        self.tokenizer = AutoTokenizer.from_pretrained(
            self.model_path, 
            trust_remote_code=True
        )
        if self.tokenizer.pad_token is None:
            self.tokenizer.pad_token = self.tokenizer.eos_token
        print(f"词汇量: {len(self.tokenizer)}")
        return self.tokenizer
    
    def load_model_optimized(self):
        print(f"\n=== 优化加载: {self.model_path} ===")

        # 1. CPU 加载
        t0 = time.perf_counter()
        cpu_model = AutoModelForCausalLM.from_pretrained(
            self.model_path,
            dtype=torch.float16,
            device_map="cpu",
            trust_remote_code=True
        )
        t1 = time.perf_counter()
        cpu_load_time = t1 - t0

        # 2. 收集权重（与 benchmark 一致）
        print("步骤2: 零拷贝权重提取...")
        t2 = time.perf_counter()
        params = list(cpu_model.parameters())
        cpu_weights = [p.detach() for p in params]                # <-- 保留列表，不在这里 del
        total_bytes = sum(w.numel() * w.element_size() for w in cpu_weights)
        t3 = time.perf_counter()
        extract_time = t3 - t2

        print(f"CPU加载: {cpu_load_time:.3f}s")
        print(f"权重提取: {extract_time:.3f}s")
        print(f"模型大小: {human_size(total_bytes)}")
        print(f"权重数量: {len(cpu_weights)}")

        # 3. 等待用户确认
        print("\n⏸️  准备开始GPU传输")
        print("请检查GPU显存使用情况 (nvidia-smi)")
        while True:
            if input("\n继续GPU传输? (y/n): ").strip().lower() == 'y':
                end_to_end_start_time = time.perf_counter()
                print("🚀 开始端到端计时 (到首token流出)...")
                break
            else:
                print("用户取消或输入非 y，退出")
                return None

        # 4. 批量 H2D（对齐 benchmark）
        print(f"\n步骤3: 高效传输 (批大小={self.batch_size})...")
        print(f"权重数量: {len(cpu_weights)}")
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

        # 5. 轻量“重建”
        t6 = time.perf_counter()
        device_obj = torch.device(self.device)
        cpu_model._device = device_obj
        for m in cpu_model.modules():
            setattr(m, "_device", device_obj)
        self.model = cpu_model
        t7 = time.perf_counter()
        rebuild_time = t7 - t6
        total_time = t7 - t0

        # === 不在这里 del；把 cpu_weights 留到首 token 后再清理 ===
        # 存下来以便后续异步清理（避免 TTFT 路径被阻塞）
        self._pending_cleanup = {
            "cpu_weights": cpu_weights,
            "params": params,
        }

        print("[[[return]]]", time.perf_counter() - end_to_end_start_time)

        return {
            'total_bytes': total_bytes,
            'cpu_load_time': cpu_load_time,
            'extract_time': extract_time,
            'transfer_time': transfer_time,
            'rebuild_time': rebuild_time,
            'total_time': total_time,
            'bandwidth': bandwidth,
            'end_to_end_start_time': end_to_end_start_time,
        }
        
    def _start_deferred_cleanup(self):
        payload = getattr(self, "_pending_cleanup", None)
        if not payload:
            return

        def _cleanup():
            try:
                cpu_weights = payload.get("cpu_weights") or []
                # 分批清空，避免一次性长停顿
                B = 512  # 每批次删除的 tensor 数；可按权重数量调大/调小
                for i in range(0, len(cpu_weights), B):
                    batch = cpu_weights[i:i+B]
                    # 通过就地置 None + 清理切片引用来加速释放
                    for j in range(len(batch)):
                        batch[j] = None
                    # 让 Python 有机会切换
                    time.sleep(0)  # 让出 GIL 片刻，降低长时间占用
                # 清空列表本体
                cpu_weights.clear()

                # 还可以断开 params 的强引用（不是必须）
                params = payload.get("params")
                if params:
                    # 不要清空 .data（模型还要用）；这里只是把 Python 列表本身释放
                    payload["params"] = None

                # 最后做一次 GC
                gc.collect()
            finally:
                # 标记完成，避免重复执行
                self._pending_cleanup = None

        t = Thread(target=_cleanup, daemon=True)
        t.start()
    
    def test_streaming_first_token(self, prompt, end_to_end_start_time):
        """测试流式生成的首token时间"""
        print(f"\n=== 流式首Token测试 ===")
        print(f"测试提示: {prompt}")
        
        if self.model is None:
            raise ValueError("模型未加载")
        
        self.model.eval()
        
        # 编码输入
        inputs = self.tokenizer(prompt, return_tensors="pt").to(self.device)
        
        # 创建流式生成器
        streamer = TextIteratorStreamer(
            self.tokenizer, 
            skip_prompt=True, 
            skip_special_tokens=True
        )
        
        # 准备生成参数
        generation_kwargs = {
            **inputs,
            'max_new_tokens': 50,
            'do_sample': False,
            'temperature': None,
            'top_p': None,
            'top_k': None,
            'pad_token_id': self.tokenizer.eos_token_id,
            'streamer': streamer
        }
        
        try:
            # 启动生成线程
            generation_thread = Thread(target=self.model.generate, kwargs=generation_kwargs)
            
            # 开始计时和生成
            stream_start = time.perf_counter()
            generation_thread.start()
            
            # 等待第一个token
            first_token_text = ""
            first_token_time = None
            stream_only_time = None
            
            for i, token_text in enumerate(streamer):
                if i == 0:  # 第一个token
                    torch.cuda.synchronize()  # 确保GPU操作完成
                    first_token_time = time.perf_counter() - end_to_end_start_time
                    stream_only_time = time.perf_counter() - stream_start
                    first_token_text = token_text
                    
                    print(f"🎯 端到端时间 (y确认→首token流出): {first_token_time:.3f}s")
                    print(f"   纯生成时间: {stream_only_time:.3f}s")
                    print(f"   首个流出token: '{first_token_text}'")
                    break
            
            # 等待生成线程完成
            generation_thread.join()
            
            if first_token_time is None:
                raise Exception("未能捕获首token")
            
            return {
                'first_token_time': first_token_time,
                'stream_only_time': stream_only_time,
                'first_token_text': first_token_text,
                'method': 'streaming'
            }
            
        except Exception as e:
            print(f"⚠️ 流式生成失败: {e}")
            print("回退到单token生成...")
            return self.test_single_token_fallback(prompt, end_to_end_start_time)
    
    def test_single_token_fallback(self, prompt, end_to_end_start_time):
        """回退方案：单token生成"""
        print(f"\n=== 单Token生成回退 ===")
        
        inputs = self.tokenizer(prompt, return_tensors="pt").to(self.device)
        
        with torch.no_grad():
            generation_start = time.perf_counter()
            
            outputs = self.model.generate(
                **inputs,
                max_new_tokens=1,
                do_sample=False,
                pad_token_id=self.tokenizer.eos_token_id
            )
            
            torch.cuda.synchronize()
            
            first_token_time = time.perf_counter() - end_to_end_start_time
            generation_only_time = time.perf_counter() - generation_start
            
            # 提取生成的token
            generated_token_id = outputs[0][-1].item()
            first_token_text = self.tokenizer.decode([generated_token_id], skip_special_tokens=True)
            
            print(f"🎯 端到端时间 (y确认→首token): {first_token_time:.3f}s")
            print(f"   纯生成时间: {generation_only_time:.3f}s")
            print(f"   生成的首token: '{first_token_text}'")
            
            return {
                'first_token_time': first_token_time,
                'generation_only_time': generation_only_time,
                'first_token_text': first_token_text,
                'method': 'single_token'
            }
    
    def test_regular_inference(self, prompts):
        """常规推理测试"""
        print(f"\n=== 常规推理测试 ===")
        
        results = []
        for i, prompt in enumerate(prompts):
            print(f"\n测试 {i+1}: {prompt}")
            
            inputs = self.tokenizer(prompt, return_tensors="pt").to(self.device)
            
            with torch.no_grad():
                t0 = time.perf_counter()
                
                outputs = self.model.generate(
                    **inputs,
                    max_new_tokens=25,
                    do_sample=False,
                    pad_token_id=self.tokenizer.eos_token_id
                )
                torch.cuda.synchronize()
                
                t1 = time.perf_counter()
            
            response = self.tokenizer.decode(outputs[0], skip_special_tokens=True)
            inference_time = t1 - t0
            new_tokens = outputs[0].shape[0] - inputs['input_ids'].shape[1]
            tokens_per_sec = new_tokens / inference_time if inference_time > 0 else 0
            
            print(f"推理时间: {inference_time:.3f}s, 速度: {tokens_per_sec:.1f} tok/s")
            print(f"输出: {response}")
            
            results.append({
                'inference_time': inference_time,
                'tokens_per_sec': tokens_per_sec,
                'new_tokens': new_tokens
            })
        
        return results
    
    def test_inference(self, load_stats):
        """测试推理和端到端性能"""
        print(f"\n=== 推理测试 ===")
        
        if self.model is None:
            raise ValueError("模型未加载")
        
        # 首token测试
        end_to_end_start_time = load_stats.get('end_to_end_start_time')
        first_token_result = None
        
        if end_to_end_start_time is not None:
            test_prompt = "What is artificial intelligence?"
            first_token_result = self.test_streaming_first_token(test_prompt, end_to_end_start_time)
        
        # 常规推理测试
        regular_prompts = [
            "解释机器学习的基本概念",
            "计算 8 * 7 = ",
            "写一个Python函数"
        ]
        
        regular_results = self.test_regular_inference(regular_prompts)
        
        return {
            'first_token_result': first_token_result,
            'regular_results': regular_results
        }
    
    def print_report(self, load_stats, inference_results):
        """打印性能报告"""
        print(f"\n=== 性能报告 ===")
        print(f"模型: {self.model_path}")
        print(f"大小: {human_size(load_stats['total_bytes'])}")
        print(f"批大小: {self.batch_size}")
        
        print(f"\n加载性能:")
        print(f"  传输带宽: {load_stats['bandwidth']:.2f} GB/s")
        print(f"  硬件效率: {load_stats['bandwidth']/12.55*100:.1f}%")
        print(f"  总加载时间: {load_stats['total_time']:.3f}s")
        
        # 首token性能分析
        first_token_result = inference_results.get('first_token_result')
        if first_token_result:
            first_token_time = first_token_result['first_token_time']
            method = first_token_result.get('method', 'unknown')
            
            if method == 'streaming':
                pure_inference_time = first_token_result.get('stream_only_time', 0)
                inference_label = "纯流式生成"
            else:
                pure_inference_time = first_token_result.get('generation_only_time', 0)
                inference_label = "纯生成时间"
            
            print(f"\n🎯 首Token性能 (关键指标) - {method}模式:")
            print(f"  端到端时间: {first_token_time:.3f}s")
            print(f"  {inference_label}: {pure_inference_time:.3f}s")
            print(f"  加载+启动开销: {first_token_time - pure_inference_time:.3f}s")
            print(f"  生成Token: '{first_token_result['first_token_text']}'")
            
            # 分析性能瓶颈
            loading_phase = load_stats['total_time']
            startup_overhead = first_token_time - loading_phase - pure_inference_time
            
            print(f"\n性能分解:")
            print(f"  1. 模型加载: {loading_phase:.3f}s ({loading_phase/first_token_time*100:.1f}%)")
            print(f"  2. 启动开销: {startup_overhead:.3f}s ({startup_overhead/first_token_time*100:.1f}%)")
            print(f"  3. 实际推理: {pure_inference_time:.3f}s ({pure_inference_time/first_token_time*100:.1f}%)")
            print(f"  总计: {first_token_time:.3f}s")
            
            if method == 'streaming':
                print(f"\n📡 流式生成优势:")
                print(f"  - 用户感知延迟: {first_token_time:.3f}s (首token出现)")
                print(f"  - 后续token将持续流出")
                print(f"  - 更好的用户体验 (无需等待完整响应)")
        
        # 常规推理性能
        regular_results = inference_results.get('regular_results', [])
        if regular_results:
            valid_results = [r for r in regular_results if r['tokens_per_sec'] > 0]
            if valid_results:
                avg_speed = sum(r['tokens_per_sec'] for r in valid_results) / len(valid_results)
                avg_time = sum(r['inference_time'] for r in valid_results) / len(valid_results)
                print(f"\n常规推理性能:")
                print(f"  平均速度: {avg_speed:.1f} tokens/s")
                print(f"  平均时间: {avg_time:.3f}s")
        
        print(f"\n✅ 测试完成")
        
        # 优化建议
        if first_token_result:
            method = first_token_result.get('method', 'unknown')
            pure_inference_time = (first_token_result.get('stream_only_time') or 
                                 first_token_result.get('generation_only_time', 0))
            loading_phase = load_stats['total_time']
            startup_overhead = first_token_time - loading_phase - pure_inference_time
            
            print(f"\n💡 优化建议:")
            if startup_overhead > 0.1:
                print(f"  - 启动开销较大({startup_overhead:.3f}s)，考虑模型预热")
            if pure_inference_time > 0.05:
                print(f"  - 推理时间较长，考虑量化或更小模型")
            if loading_phase > first_token_time * 0.7:
                print(f"  - 加载时间占比大，模型传输已优化")
            if method == 'streaming':
                print(f"  ✅ 已使用流式生成，用户体验最优")
    
    def run_test(self):
        """运行完整测试"""
        print(f"=== 流式首Token时间测试: {self.model_path} ===")
        print(f"📊 测量指标：TTFT (Time to First Token)")
        print(f"🎯 目标：用户感知到的首个token流出时间")
        
        # 加载tokenizer
        self.load_tokenizer()
        
        # 优化加载模型
        load_stats = self.load_model_optimized()
        print("[[[load_stats]]]", time.perf_counter() - load_stats['end_to_end_start_time'])
        # self._start_deferred_cleanup()
        if load_stats is None:
            print("用户取消测试")
            return
        
        # 推理测试
        inference_results = self.test_inference(load_stats)
        
        # 打印报告
        self.print_report(load_stats, inference_results)
        
        # 等待用户确认清理
        print(f"\n{'='*50}")
        print("🔍 第一轮测试已完成")
        print("📊 请检查显存使用情况 (nvidia-smi)")
        print("💡 确认后将开始清理资源，您可以观察显存释放过程")
        print(f"{'='*50}")
        
        # while True:
        #     user_input = input("\n确认开始清理? (y/n): ").strip().lower()
        #     if user_input == 'y':
        #         print("\n🧹 开始清理资源...")
        #         # 执行清理操作
        #         self.cleanup()
        #         print("✅ 资源清理完成")
                
        #         # 询问是否重新运行测试
        #         print(f"\n{'='*50}")
        #         print("🔄 显存已释放，可以重新运行测试")
        #         print("📊 请再次检查显存使用情况 (nvidia-smi)")
        #         print(f"{'='*50}")
                
        #         while True:
        #             rerun_input = input("\n重新运行测试? (y/n): ").strip().lower()
        #             if rerun_input == 'y':
        #                 print("\n🔄 开始重新运行测试...")
        #                 # 重新加载tokenizer和模型，再次运行测试
        #                 self.load_tokenizer()
        #                 rerun_load_stats = self.load_model_optimized()
        #                 if rerun_load_stats is not None:
        #                     rerun_inference_results = self.test_inference(rerun_load_stats)
        #                     self.print_report(rerun_load_stats, rerun_inference_results)
        #                     print("\n🎯 第二轮测试完成")
        #                 return load_stats, inference_results
        #             elif rerun_input == 'n':
        #                 print("测试结束")
        #                 return load_stats, inference_results
        #             else:
        #                 print("请输入 y 或 n")
                        
        #     elif user_input == 'n':
        #         print("用户取消清理，保持当前状态")
        #         return load_stats, inference_results
        #     else:
        #         print("请输入 y 或 n")
    
    def cleanup(self):
        """清理资源"""
        print("  正在清理模型...")
        if self.model is not None:
            del self.model
            self.model = None
        
        print("  正在清理tokenizer...")
        if self.tokenizer is not None:
            del self.tokenizer
            self.tokenizer = None
            
        print("  正在清理缓存...")
        # 清理待处理的cleanup任务
        if hasattr(self, '_pending_cleanup'):
            self._pending_cleanup = None
            
        # 强制垃圾回收
        gc.collect()
        print("Before reset:", torch.cuda.is_available())
        
        
        print("  正在重置CUDA设备...")
        libcudart.cudaDeviceReset()
        
        # torch.cuda._lazy_init()
        # print("PyTorch CUDA reinitialized")
        
        print("After reset:", torch.cuda.is_available())
        
        
        print("  显存清理完成")

def main():
    parser = argparse.ArgumentParser(description="详细性能分析 - 定位TTFT瓶颈点")
    parser.add_argument("--model", default="Qwen/Qwen3-14B", help="模型路径")
    parser.add_argument("--device", default="cuda:0", help="GPU设备")
    parser.add_argument("--batch-size", type=int, default=20, help="批量大小")
    
    args = parser.parse_args()
    
    loader = StreamingTokenLoader(args.model, args.device, args.batch_size)
    
    try:
        loader.run_test()
    except Exception as e:
        print(f"测试失败: {e}")
        import traceback
        traceback.print_exc()
    finally:
        loader.cleanup()

if __name__ == "__main__":
    main()