# Neutree Inference Control Plane (`neutree-gateway`) Trial Guide

`neutree-gateway` is a lightweight Go inference gateway and control-plane engine that runs side-by-side with an existing Neutree deployment without modifying `db/migrations/` or `internal/gateway/`.

## Architecture Overview

- `internal/icp/state`: Unified event envelope (`config`, `quota`, `runtime`), lock-free `atomic.Pointer[ConfigSnapshot]` reads paired with a concurrent TTL/in-flight `RuntimeTable`, `APIKey -> PolicyGroup -> AllowedRoutes` policy evaluation, and read-only `PostgRESTSyncer` (`endpoints`, `clusters`, `external_endpoints`, `api_keys`, `group_policies` over PostgREST `api` schema).
- `internal/icp/quota`: Protocol-neutral `UsageRecord` (`RequestID`, `AttemptID`, `Measures`), 3-layer usage extraction (`X-Neutree-Usage` response header, `event: neutree.usage` SSE tail frame, and HMAC-signed `POST /internal/v1/usage` async callback), and `LeasePool` with mid-stream step charging and exhaustion cutoff.
- `internal/icp/protocol`: Canonical block-level IR (`CanonicalRequest`, `CanonicalResponse`, `StreamEvent`) and bidirectional codecs across `OpenAI Chat`, `Anthropic Messages`, and `OpenAI Responses` (streaming SSE and non-streaming JSON).
- `internal/icp/proxy`: Dual-track HTTP/1.1 + HTTP/2 (`h2c`) data plane:
  - **Pre-body unmanaged fallback**: Routes not owned by `--managed-routes` stream zero-buffered directly to `--legacy-kong-url` with original headers, credentials, and `multipart` bodies intact.
  - **Zero-buffer passthrough track**: Multimodal `multipart`, binary/audio, and custom SSE streams bypass JSON buffering while still applying auth, prefix-hash salting, and neutral usage stripping.
  - **Translated failover track**: Selects healthy upstreams by saturation, `prefix-cache` affinity (`X-Neutree-Prefix-Hash` or canonical prompt prefix), priority, and `(QueueDepth + InFlight)`, recompiling protocol and credentials across heterogeneous providers before the first response byte is sent.
  - **Three-plane plugin lifecycle (`RequestHook` / `AttemptHook` / `SemanticHook`)**: Decouples per-request session plugins (`TelemetryPlugin`, `AccessPlugin` with dynamic `/v1/models` & `/anthropic/v1/models` filtering and per-key concurrency/rate limits, `GuardrailPlugin` via `--guardrail-block` / `--guardrail-redact`, and `QuotaPlugin`) with forward `0..k` entry and guaranteed LIFO `k..0` completion.

## Option A: Instant Trial from Source on a Linux Host (No Image Registry Needed)

On the Linux host running the Neutree control plane, compile a static Linux binary locally and mount it directly into `alpine:3.20` (which includes `wget` for `/healthz` checks) on the existing `neutree` Docker network:

```sh
CGO_ENABLED=0 GOOS=linux go build -o /tmp/neutree-gateway ./cmd/neutree-gateway

docker run -d --name neutree-gateway \
  --network neutree-core_neutree \
  -p 8080:8080 \
  -v /tmp/neutree-gateway:/usr/local/bin/neutree-gateway:ro \
  alpine:3.20 /usr/local/bin/neutree-gateway \
  --listen=:8080 \
  --admin-listen=127.0.0.1:8081 \
  --legacy-kong-url=http://kong:8000 \
  --postgrest-url=http://postgrest:6432 \
  --storage-jwt-secret="${NEUTREE_JWT_SECRET}" \
  --managed-routes='*' \
  --callback-secret="${ICP_CALLBACK_SECRET}"
```

*(Note: check `docker network ls | grep neutree` if your Compose project network has a different prefix such as `neutree`.)*

## Option B: Docker Compose Override

Attach `neutree-gateway` onto an existing Neutree control-plane Docker Compose stack (where `docker-compose.yml` has been rendered into `${NEUTREE_WORKDIR}`, e.g. `/var/lib/neutree/neutree-core`):

```sh
export NEUTREE_JWT_SECRET='<same-jwt-secret-as-postgrest>'
export ICP_CALLBACK_SECRET='<random-hmac-secret-for-async-usage>'
export ICP_MANAGED_ROUTES='none' # start in pure Kong pass-through mode, or '*' for all synced routes

docker compose \
  -f "${NEUTREE_WORKDIR}/docker-compose.yml" \
  -f deploy/docker/icp-poc/compose.override.yaml \
  up -d neutree-gateway
```

Kong remains untouched on port `80`, while `neutree-gateway` listens on `:8080` (data plane) and `127.0.0.1:8081` inside the container (admin plane, never exposed on `:8080`).

## Live Canary Cutover & Inspection

1. **Health & active snapshot / usage ledger** (from inside the container or SSH loopback tunnel):
   ```sh
   curl -fsS http://127.0.0.1:8080/healthz
   docker exec neutree-gateway wget -qO- http://127.0.0.1:8081/admin/v1/snapshot
   docker exec neutree-gateway wget -qO- http://127.0.0.1:8081/admin/v1/usage
   docker exec neutree-gateway wget -qO- http://127.0.0.1:8081/admin/v1/telemetry
   ```

2. **Cut over a single route dynamically in milliseconds** (other routes continue streaming to Kong):
   ```sh
   docker exec neutree-gateway wget -qO- --header='Content-Type: application/json' \
     --post-data='{"kind":"config","config":{"managed_routes":{"/workspace/default/external-endpoint/demo":true}}}' \
     http://127.0.0.1:8081/admin/v1/events
   ```

3. **Push runtime `prefix-cache` affinity or health signals**:
   ```sh
   docker exec neutree-gateway wget -qO- --header='Content-Type: application/json' \
     --post-data='{"kind":"runtime","runtime":{"upstream_id":"endpoint:default/qwen","healthy":true,"queue_depth":1,"prefix_hashes":["default:/workspace/default/endpoint/qwen:session-42"],"expires_at":"2099-01-01T00:00:00Z"}}' \
     http://127.0.0.1:8081/admin/v1/events
   ```

4. **Send traffic through `:8080`** using any existing Neutree API key (`Authorization: Bearer <sk-...>` or `x-api-key`):
   ```sh
   curl -N http://127.0.0.1:8080/workspace/default/external-endpoint/demo/v1/responses \
     -H "Authorization: Bearer ${NEUTREE_API_KEY}" \
     -H "Content-Type: application/json" \
     -d '{"model":"claude-sonnet","stream":true,"input":"Hello from Responses API"}'
   ```
