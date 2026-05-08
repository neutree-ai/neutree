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

For everything else, open the relevant file under [Files in this Directory](#files-in-this-directory).

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

