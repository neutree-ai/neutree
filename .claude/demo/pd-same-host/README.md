# PD Same-Host Phase 0 Demo

End-to-end vertical slice that validates the API → IR → Strategy → Orchestrator
→ Ray Application path before MVP scope commits portalloc / CHWBL /
check_health etc.

## Files

- `endpoint.json` — sample EndpointSpec body (PD same-host 1P×1D)
- `create.sh`     — POST /api/v1/endpoints
- `verify.sh`     — poll status, hit /v1/models, smoke /v1/chat/completions
- `perf.sh`       — naive PD vs monolithic baseline throughput (optional)
- `report.md`     — V1–V9 validation report template (fill after running)

## Prerequisites

| Item | Value | Notes |
|---|---|---|
| Neutree CP | running on local CP or LAN CP | `NO_PROXY=10.255.1.54,...` if behind proxy |
| SSH cluster | registered + Ray runtime online | `neutree-cli cluster list` shows Ready |
| Engine image | `neutree/engine-vllm:v0.17.1-pd` | built via `cluster-image-builder/Dockerfile.engine-vllm-pd` |
| EngineVersion | `vllm@v0.17.1` registered | comes from `internal/engine/builtin.go` |
| GPU node | 2 GPU NVLink, fabric manager running | required for V5/V9 |

## Run

```bash
# 0. Build the PD engine image (one time)
make -C cluster-image-builder build-engine-vllm-pd  # TBD make target; or manual:
#   docker build -f cluster-image-builder/Dockerfile.engine-vllm-pd \
#     --build-arg ENGINE_BASE_IMAGE=vllm/vllm-openai:v0.17.1 \
#     --build-arg ENGINE_VERSION_DIR=v0_17_1 \
#     --build-arg RAY_REPO=https://github.com/neutree-ai/ray \
#     --build-arg RAY_COMMIT=$(grep '^RAY_COMMIT' cluster-image-builder/Makefile | awk '{print $3}') \
#     -t neutree/engine-vllm:v0.17.1-pd cluster-image-builder

# 1. Submit endpoint
NEUTREE_API_URL=http://10.255.1.54:3000 \
NEUTREE_API_KEY=...  \
CLUSTER=my-ssh-cluster \
./.claude/demo/pd-same-host/create.sh

# 2. Wait for Running + smoke
./.claude/demo/pd-same-host/verify.sh

# 3. (optional) baseline perf
./.claude/demo/pd-same-host/perf.sh
```

## What this validates (V1–V9)

See `report.md`. The Demo only sets up enough scaffolding to observe each
assumption. Pass/fail goes back into MVP planning per the 00 overview doc.
