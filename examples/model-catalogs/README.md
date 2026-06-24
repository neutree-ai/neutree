# Model Catalog examples (recipe form)

Sample recipe-shape ModelCatalogs converted from the upstream vLLM recipe
site (https://recipes.vllm.ai/), usable as test data. Import via the UI
(Model Catalogs → Import → Paste YAML / Upload) or the API:

    POST /api/v1/model_catalogs/import   { "yaml": "<file contents>", "workspace": "default" }

To make them **deployable**, first create the public HuggingFace model registry
they reference (every variant's `model.registry` is `huggingface`):

    POST /api/v1/model_registries   (body = ../model-registries/huggingface.yaml as JSON)

Without it the catalogs still import and render, but a deploy can't resolve the
model source.

| File | Source recipe | Shape |
|---|---|---|
| `qwen3.6-27b.yaml` | [Qwen3.6-27B](https://recipes.vllm.ai/Qwen/Qwen3.6-27B) | dense 27B, BF16 + FP8 variants |
| `qwen3.6-35b-a3b.yaml` | [Qwen3.6-35B-A3B](https://recipes.vllm.ai/Qwen/Qwen3.6-35B-A3B) | MoE 35B/A3B, BF16 + FP8 + NVFP4 variants |
| `qwen3.6-27b-zh.yaml` / `qwen3.6-35b-a3b-zh.yaml` | 同上 | Chinese-localized copies (`-zh`) — identical config, Chinese `display_name`/descriptions for EN↔CN comparison |

Each carries per-variant model display info (parameter_count / quantization /
context_length / architecture), `vram_minimum_gb`, the
`recipe.vllm.ai/hardware-verified` annotation, and features of all three types:

- **boolean** toggles (`text-only`, `disable-thinking`, `tool-calling`, …; some
  marked `category: tuning`),
- an **input** `max-model-len` (context window) with `suggestions`
  (8K / 32K / 128K / 256K) — rendered as a "pick a preset or type your own"
  combobox (select + free input),
- an **input** `max-num-seqs` (free integer — decode parallelism / batch width).

Every feature carries a `display_name` aligned with the deploy mockup's wording
(上下文窗口 = Context window, 并发数 = Concurrency, …); the UI shows that label
with the technical key as a hint. The vLLM `--flags` map to underscored
`engine_args` keys; an input feature's `${value}` placeholder is filled by the
user value (coerced to `value_type`).
