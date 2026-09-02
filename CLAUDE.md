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

See [`contributing/README.md`](contributing/README.md) for the engineering guide — first-time setup, playbooks, and a file-by-file index of `contributing/`.

## Engine Package Release Rule

For Engine image and package release changes, `engine_patch_suffix` is an
optional raw Docker tag segment without a leading `-`. Empty keeps the upstream
version; a non-empty value is appended exactly once as `-<suffix>` and must
never receive an implicit `neutree` prefix. Derive image tags, manifest
versions, archive/checksum names, and package URLs from the same suffix
semantics. Preserve legacy `-neutree*` names only when callers explicitly pass
that value, and update the matching enterprise release path plus required
static checks and Step 5 release evidence whenever this contract changes.
