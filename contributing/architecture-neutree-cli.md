# neutree-cli

## Process Role

`neutree-cli` is **not** a runtime control-plane component. It's an operator-facing tool that runs on a workstation or jump host and drives:

- **Install / upgrade Neutree** — bootstrap the operational stack (`launch neutree-core`) and observability backends (`launch obs-stack`) via Docker Compose, locally or over SSH.
- **Resource CRUD** — apply / get / delete / wait on Neutree resources by talking to a deployed `neutree-api` over HTTP.
- **Engine package import** — register an engine into a running control plane from a `.tar.gz` / `manifest.yaml`.

It does **not**:
- run reconcile loops,
- expose HTTP endpoints,
- get embedded into `neutree-api` or `neutree-core`.

Entry point: `cmd/neutree-cli/neutree-cli.go` → `cmd/neutree-cli/app/cmd/cmd.go`.

## Subcommand Groups

Subcommands live under `cmd/neutree-cli/app/cmd/`. Each top-level directory is a Cobra subcommand group; resource-aware verbs branch by resource type internally (so adding a resource means touching each verb — see [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type)).

| Subcommand group | Purpose |
|------------------|---------|
| `apply` | Create/update resources from a YAML/JSON manifest. |
| `get` | Read resources (single or list). |
| `delete` | Soft-delete a resource (sets `deletion_timestamp`; `neutree-core` finishes the job). |
| `wait` | Block until a resource reaches a terminal status — used heavily by E2E and install flows. |
| `cleanup` | Tear down a compose stack deployed by `launch` (`cleanup neutree-core` or `cleanup obs-stack`); `--remove-data` also drops volumes. |
| `launch` | Two subcommands: `launch neutree-core` (operational stack — neutree-api + neutree-core + Postgres/GoTrue/PostgREST + Kong + vmagent + vector) and `launch obs-stack` (VictoriaMetrics + Grafana). Both via Docker Compose. |
| `engine` | Engine-specific operations (import a package, list versions, ...). |
| `image` | Push an arbitrary container image into a Neutree-managed image registry (`image push`). |
| `model` | Model registry / catalog operations. |
| `packageimport` | Import engine packages and similar bundles. |
| `resource` | Generic resource operations not tied to a single verb. |
| `global` | Persistent flags shared across all subcommands. |

`version.go` holds the embedded version metadata.

## Authentication

Every subcommand that talks to a deployed `neutree-api` requires:

- `--server-url` — the API URL.
- `--api-key` (or env `NEUTREE_API_KEY`) — an API key created via `neutree-api` and resolved by `cmd/neutree-cli/app/cmd/global/global.go`.

The CLI sends the API key on `Authorization`; `neutree-api`'s `auth` middleware decrypts it and converts to a PostgREST JWT in-flight (see [`architecture-neutree-api.md#authentication`](architecture-neutree-api.md#authentication)). The CLI itself never sees a JWT.

`--insecure` disables TLS verification for local / self-signed deployments.

## Deploy Mode

`launch` installs Neutree via **Docker Compose**. The two subcommands render compose files under `deploy/docker/` and run `docker compose up -d` — locally on the operator's host or over SSH on a target node:

- `launch neutree-core` → `deploy/docker/neutree-core/docker-compose.yml`
- `launch obs-stack` → `deploy/docker/obs-stack/docker-compose.yml`

There is currently no Kubernetes-native install path in `neutree-cli`; the Helm chart at `deploy/chart/` is applied with `helm install` directly by operators outside this tool.

Note: this is independent from the runtime cluster mode (K8s vs SSH) — the runtime mode controls how user `Endpoint` resources are scheduled (`Cluster.spec.config`), not how the CP itself is installed.

## Arbitrary Image Push

`neutree-cli image push <image>` tags a local Docker image into the `ImageRegistry` resource
named by `--image-registry` / `--workspace` and pushes it. The printed reference is what the
platform will pull, which makes it usable verbatim as an engine `image` override (the flex
engine's `engine_args.image`). Credentials come from the registry's stored auth via the
`credentials/image_registries` endpoint (`image_registry:read-credentials`), with
`--registry-username` / `--registry-password` as the fallback for keys that lack the permission.

Neutree only requires a registry's stored credentials to grant **pull** — the account behind them
may well be unable to push. That failure comes back from the registry as an opaque string, so
`explainPushFailure` classifies it by which credentials were actually used (stored / flags /
anonymous / unreadable) and names the thing to change: update the `ImageRegistry` resource, or pass
`--registry-username` / `--registry-password`. Failures that are not authentication failures are
returned untouched, so a missing local image is never misreported as a permission problem.

The target reference uses `util.RelocateImageRef`, **not** the `util.RewriteImageRef` the
orchestrators render pull specs with. The two agree except on Docker Hub prefixes: `RewriteImageRef`
deliberately leaves a reference alone when the registry is Docker Hub (already pullable, no prefix
needed), which on the push side would send the image to whatever registry the *source* reference
names — a different host than the one whose credentials are in play. `RelocateImageRef` always
applies the prefix; `RewriteImageRef` is now the Docker-Hub short-circuit in front of it.

Implementation: `internal/cli/image/` (`push.go` workflow, `pusher.go` Docker primitives).

## Engine Package Import

`neutree-cli` can register a new engine into a running control plane from an `.tar.gz` produced by `scripts/builder/build-engine-package.sh`. The flag surface is intentionally narrow: only `-p <engine-package>` (a tarball or manifest), no per-flag overrides for name/version/image/accelerator. If a needed engine has no package available, the answer is to file a Neutree request for one rather than synthesize a manifest by hand.

## Adding CLI Behavior

For a new resource: every resource-aware verb (`apply`, `get`, `delete`, `wait`) needs a branch — see [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type) for the per-verb checklist.

For a new operator workflow (e.g., a new `launch` target): add the subcommand under the matching group and the implementation under `internal/cli/` or `internal/deploy/` — keep `cmd/neutree-cli/app/cmd/<group>/*.go` small (boundary rule **R4**: cmd/ files stay under 500 lines).
