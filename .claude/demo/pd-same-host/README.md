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
| Engine image | `neutree/engine-vllm:v0.17.1` | same image as monolithic; PD-specific knobs (UCX_TLS, fabric_manager bind mount) injected by the control plane at deploy time |
| EngineVersion | `vllm@v0.17.1` registered | comes from `internal/engine/builtin.go` |
| GPU node | 2 GPU NVLink, fabric manager running | required for V5/V9 |

## Run

```bash
# 0. Use the existing engine-vllm image (built via the normal release pipeline).
#    No PD-specific image needed — the control plane injects UCX_TLS env
#    and the /var/run/nvidia-fabricmanager bind mount at deploy time.

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

## What this validates (V1–V11)

See `report.md`. The Demo only sets up enough scaffolding to observe each
assumption. Pass/fail goes back into MVP planning per the 00 overview doc.

V10 + V11 specifically exercise the Ray-RequestRouter-as-topology-observer
pattern: `serve/_ingress_router/observer_router.py` is wired as the
`request_router_class` of `PDCollocatedBackend`, and PDIngress reads the
resulting `_SHARED` view via `GET /v1/topology`. If V10 fails the MVP
`_SHARED + ObserverRouter` design needs reconsideration before
`PR-ingress-lib`.
