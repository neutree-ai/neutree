# Coding Standards

> Mechanical rules enforced by `golangci-lint` and commit conventions enforced by review. These should rarely require human judgment — if a change trips a linter, fix the code, don't silence the linter.

## golangci-lint (25 linters enabled)

Config: [`.golangci.yaml`](../.golangci.yaml). Runs on `make lint` locally and inside CI (`pr-check` job).

| Linter | Threshold | Typical trigger |
|--------|-----------|-----------------|
| `lll` | 170 chars / line | Long `if` conditions, long call chains |
| `funlen` | 250 lines / 100 statements per function | Oversized reconcilers |
| `gocyclo` | complexity 30 | Deep nesting |
| `dupl` | 400 tokens | Copy-paste |
| `goconst` | string repeated 3+ times | Literals should become package constants |
| `gosec` | G115 globally excluded | Integer-overflow warnings |
| `errcheck` | `check-type-assertions: true` | `x.(T)` without `ok` |

See the [Lint Error Cheatsheet](#lint-error-cheatsheet) below for fix directions on the most common errors.

## Lint Error Cheatsheet

| Error | Typical trigger | Fix direction |
|-------|-----------------|---------------|
| `lll: line is 170+` | Long `if` / long call chains | Extract variables; split across lines |
| `funlen: lines 250+ / statements 100+` | Oversized reconciler | Extract helpers; single responsibility |
| `gocyclo: complexity 30+` | Deep nesting | Early return; strategy pattern |
| `dupl: duplicate of ...` | Copy-paste | Lift to the same package or `pkg/util/` |
| `goconst: repeated 3+ times` | String literal | Promote to a package-level constant |
| `gosec: G115` | Integer overflow | Globally excluded — no action needed |
| `errcheck: type assertion` | `x.(T)` without `ok` | Rewrite as `x, ok := x.(T); if !ok { ... }` |

## Import Organization

`goimports` groups imports as: stdlib → third-party → `github.com/neutree-ai/neutree` (local-prefix). Configured via `linters-settings.goimports.local-prefixes` in `.golangci.yaml`.

## Commit Convention

Conventional commits with squash-merge — **one commit per PR**.

- Prefixes: `fix:`, `feat:`, `chore:`, `refactor:`, `docs:`, `test:`, `ci:`, `improve:`, `perf:`.
- Format: `<type>: <imperative description> (#PR)`.
- Every commit must include the PR reference `(#NNN)`.
- Example: `fix: gate ENGINE_NAME/ENGINE_VERSION env vars behind new cluster version check (#357)`.

## Pull Request

- One logical change per PR; split unrelated changes into separate PRs.
- The squash-merge commit inherits the PR title — write the PR title in the commit convention above.
- Fill out every checkbox in `.github/pull_request_template.md` before requesting review.
