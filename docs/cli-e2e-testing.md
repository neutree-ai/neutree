# CLI E2E Testing Guide

Black-box E2E tests for `neutree-cli`. Tests compile the binary, execute it against a live Neutree server via `os/exec`, and assert on stdout/stderr/exit code.

## Running Tests

```bash
# Use .env file (auto-sourced by Makefile) or export manually
cp tests/e2e/.env.example .env

make e2e-test                              # all tests
make e2e-test LABEL_FILTER="push"          # by feature
make e2e-test LABEL_FILTER="C2612561"      # by TestRail case
```

## Structure

```
tests/e2e/
├── e2e_suite_test.go       # Suite orchestration (rarely changes)
├── helpers.go              # RunCLI, assertions, cleanup, template rendering
├── testrail.go             # TestRail API reporter
├── model_test.go           # Model management specs
└── testdata/
    └── model-registry.yaml # YAML template with ${VAR} placeholders
```

## Lifecycle

```
BeforeSuite: go build CLI → apply -f testdata YAML → wait for Ready
Tests:       RunCLI(...) → assert CLIResult { Stdout, Stderr, ExitCode }
AfterSuite:  delete -f testdata YAML → rm CLI binary
ReportAfterSuite: scan labels for C{id} → POST to TestRail
```

`BeforeSuite` creates shared resources from YAML templates; each test creates its own data and cleans up via `DeferCleanup`.

## Writing Tests

```go
var _ = Describe("Model", func() {
    Describe("Push", Label("model", "push"), func() {
        It("should push a model", Label("C2612561"), func() {
            DeferCleanup(EnsureModelDeleted, "e2e-push-basic", "v1.0")

            r := pushModel("e2e-push-basic", "v1.0", 64)
            ExpectSuccess(r)
            ExpectStdoutContains(r, "pushed successfully")
        })
    })
})
```

**Labels**: Feature labels (`"model"`, `"push"`) for filtering; TestRail case IDs (`"C2612561"`) for auto-reporting. `Describe` labels are inherited by inner `It` blocks.

**Cleanup**: Every test that creates data must register `DeferCleanup`. Use `e2e-` prefixed names. Delete tests that verify deletion need no cleanup.

## Testdata YAML Templates

Templates use `${VAR}` placeholders expanded via `os.Expand` (env var → defaults map → empty string).

```yaml
apiVersion: v1
kind: ModelRegistry
metadata:
  name: ${E2E_MODEL_REGISTRY}
  workspace: ${E2E_WORKSPACE}
spec:
  type: bentoml
  url: ${E2E_MODEL_REGISTRY_URL}
```

Conventions:
- One resource per file, `${VAR}` for all dynamic values
- Defaults provided in code (`renderTemplate` defaults map), not in templates
- Resource names include `runID` suffix (e.g. `e2e-registry-482910`) for parallel run isolation
- Render to temp file via `renderTemplateToTempFile()` for apply/delete
- Teardown uses `--force --ignore-not-found` for idempotency
- Always `wait --for` after apply before running tests

Adding a new template: create YAML in `testdata/`, add setup/teardown function in `helpers.go`, call from BeforeSuite/AfterSuite.

## Helper API

```go
RunCLI(args ...string) CLIResult              // auto-injects --server-url, --api-key, --insecure
RunCLIWithStdin(stdin string, args ...string) CLIResult

ExpectSuccess(r CLIResult)                    // exit code 0
ExpectFailed(r CLIResult)                     // exit code != 0
ExpectStdoutContains(r CLIResult, substr)     // stdout contains substring

EnsureModelDeleted(name, version string)      // delete model, ignore errors
testRegistry() string                         // "e2e-registry-{runID}" or $E2E_MODEL_REGISTRY
testWorkspace() string                        // "default" or $E2E_WORKSPACE
pushModel(name, version string, fileSize int, extraArgs ...string) CLIResult
```

## TestRail Integration

Automatic via `ReportAfterSuite`: scans spec labels matching `C\d+`, batch-uploads results. Silently skipped when `TESTRAIL_RUN_ID` is not set.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `NEUTREE_SERVER_URL` | Yes | — | API server URL |
| `NEUTREE_API_KEY` | Yes | — | API authentication key |
| `E2E_MODEL_REGISTRY_URL` | Yes | — | Model registry storage URL |
| `E2E_MODEL_REGISTRY` | No | `e2e-registry-{runID}` | Model registry name |
| `E2E_WORKSPACE` | No | `default` | Workspace |
| `TESTRAIL_RUN_ID` | No | — | TestRail run ID (enables reporting) |
| `TESTRAIL_URL` | No | — | TestRail server URL |
| `TESTRAIL_USER` | No | — | TestRail username |
| `TESTRAIL_PASSWORD` | No | — | TestRail password |
