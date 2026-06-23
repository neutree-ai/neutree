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

Each carries per-variant model display info (parameter_count / quantization /
context_length / architecture), `vram_minimum_gb`, the
`recipe.vllm.ai/hardware-verified` annotation, and opt-in features (some marked
`category: tuning`). The vLLM `--flags` map to underscored `engine_args` keys.
