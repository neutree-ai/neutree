# Contributing to Neutree

Engineering guide for human contributors and AI agents. Start here, then open the specific file you need.

## First-time Setup

After cloning the repo, install the local git hooks:

```bash
make install-hooks
```

This enables the pre-commit gate (`gofmt` / `go vet` / architecture boundaries / migration-pair check / incremental lint / short tests on affected packages).

## Playbooks

Step-by-step checklists for the two highest-cost additions:

| Goal | Entry point | Checklist |
|------|-------------|-----------|
| Add a new version to an existing engine | `internal/engine/<name>/<version>/` + `cluster-image-builder/serve/<name>/<version>/` | 9 steps + verify — [`playbooks.md#adding-a-new-version-to-an-existing-engine`](playbooks.md#adding-a-new-version-to-an-existing-engine) |
| Add a new resource type | `api/v1/` → `db/migrations/` → `controllers/` → `internal/routes/proxies/` | 12 steps + verify — [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type) |
| Add a control-plane deployment component | `deploy/docker/` + `deploy/chart/neutree/` + `scripts/builder/image-lists/controlplane/images.txt` | Dual deployment checklist — [Adding Control-plane Deployment Components](#adding-control-plane-deployment-components) |

For everything else, open the relevant file under [Files in this Directory](#files-in-this-directory).

## Adding Control-plane Deployment Components

Neutree supports both Docker Compose and Helm chart deployments for the control plane. When adding a control-plane component, keep both deployment surfaces in sync unless the component is intentionally limited to one mode.

Required checklist:

1. Add the component to the relevant Compose stack under `deploy/docker/` (`neutree-core` or `obs-stack`), including image, environment, volumes, ports, dependencies, and config files.
2. Add the same component to `deploy/chart/neutree/`, including default values, templates, config, volumes, ports, and service account/RBAC resources if needed.
3. Add every new runtime image to `scripts/builder/image-lists/controlplane/images.txt` so offline control-plane packages include it.
4. Render the Helm chart and review the Compose template before committing.

If the component differs between Compose and Helm, document the reason in the PR description and make sure the offline image list still covers the images required by the Helm chart.

## Files in this Directory

| File | Scope |
|------|-------|
| [`architecture.md`](architecture.md) | Tech stack, project layout, layered architecture (R1–R5), data flow, core resource types — cross-cutting only |
| [`architecture-neutree-api.md`](architecture-neutree-api.md) | neutree-api process role, route topology, authentication |
| [`architecture-neutree-core.md`](architecture-neutree-core.md) | neutree-core process role, controller pattern (idempotent / spec-status / soft-delete), reconcile cadence, cluster modes, vendor plugin pairs |
| [`architecture-neutree-cli.md`](architecture-neutree-cli.md) | neutree-cli process role, subcommand groups, authentication, deploy mode, engine package import |
| [`testing.md`](testing.md) | Unit (testify + mockery), Python co-location, DB integration, E2E (Ginkgo), impl/test file pairs |
| [`coding-standards.md`](coding-standards.md) | golangci-lint rules, import organization, commit convention, lint fix cheatsheet |
| [`database.md`](database.md) | PostgREST + RLS model, migration rules (incl. pairing), auth token layers, common errors |
| [`playbooks.md`](playbooks.md) | Step-by-step checklists — new engine version, new resource type |
