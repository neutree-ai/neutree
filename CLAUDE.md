# Neutree

Open-source LLM infrastructure management platform: multi-cluster inference workload management, OpenAI-compatible API gateway, and production-grade observability.

## Ignore (do not read or grep)

- `bin/`, `out/`, `vendor/`, `tmp/`, `devtools/`
- `.vscode/`, `.idea/`
- `**/mock/`, `**/mocks/`, `**/*_mock.go`
- `deploy/docker/neutree-core/gateway/`, `deploy/docker/neutree-core/vmagent/`
- `deploy/docker/obs-stack/grafana/dashboards/`
- `deploy/chart/neutree/gateway/`, `deploy/chart/neutree/vmagent/`, `deploy/chart/neutree/grafana-dashboards/`
- `scripts/dashboard/ray-upstream/`, `scripts/dashboard/vllm-upstream/`, `scripts/dashboard/output/`
- `cluster-image-builder/downloader/`, `scripts/builder/dist/`
- `__pycache__/`, `*.out`, `*.tar`

## Where to Look

Start at [`contributing/README.md`](contributing/README.md) — it carries the task-navigation table and working rules for this repo. Topic guides:

- [`contributing/architecture.md`](contributing/architecture.md) — tech stack, project layout, controller pattern, cluster modes, data flow, resources, language distribution
- [`contributing/testing.md`](contributing/testing.md) — unit, Python co-location, DB integration, E2E (Ginkgo)
- [`contributing/coding-standards.md`](contributing/coding-standards.md) — golangci-lint rules, import organization, commit convention, lint error cheatsheet
- [`contributing/database.md`](contributing/database.md) — PostgREST + RLS model, migration rules, auth token layers, common errors
- [`contributing/playbooks.md`](contributing/playbooks.md) — step-by-step checklists for adding a new engine version or a new resource type
