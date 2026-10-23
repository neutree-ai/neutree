# RFC: CLI Benchmark for Managed Inference Endpoints

## Status

- Status: Proposed.
- Review request: approve an experimental CLI command for vLLM Endpoint
  benchmarks.
- Authoritative inputs: confirmed product requirements and the command contract
  in `cmd/neutree-cli/app/cmd/bench/` and `internal/cli/bench/`.

## Summary

This RFC proposes `neutree-cli exp bench --engine vllm ENDPOINT` as the
fast path for benchmarking a managed inference Endpoint. The operator provides
an Endpoint and workload shape; the CLI resolves the Cluster, chooses one
runtime, derives routing and tokenizer details, runs `vllm bench serve`, and
collects artifacts on the CLI host.

The benchmark client is co-located with a selected inference runtime. Requests
still use the normal Router or Ray Serve route, so the result measures the
Endpoint path seen by production callers, not direct performance of one replica.

## Motivation

Running a comparable benchmark currently requires cluster-specific manual work:
finding a runtime, obtaining kubeconfig or SSH access, deriving the route and
tokenizer path, constructing a vLLM command, and retrieving its result. The
steps differ between Kubernetes and static Ray clusters and make routine
capacity checks slow and error-prone.

The proposed command keeps that complexity in the CLI while preserving the
normal serving route and existing authorization model.

## Proposal

### Command

```bash
neutree-cli exp bench --engine vllm ENDPOINT \
  --workspace default \
  --input-lens 16384 \
  --output-lens 2048 \
  --concurrencies 1,2,4 \
  --num-prompts 20 \
  --request-rate inf \
  --output-dir ./results
```

`--engine` is required and initially accepts only `vllm`. The command accepts
only Running text-generation Endpoints backed by vLLM.

`--input-lens`, `--output-lens`, and `--concurrencies` accept comma-separated
or repeated positive integers. The CLI de-duplicates and sorts values, then
runs one case per Cartesian-product tuple. Default values are `16384` input
tokens, `2048` output tokens, concurrency `2`, `20` prompts, and request rate
`inf`.

`--output-dir` belongs to the host running `neutree-cli`. It is never a Pod or
container path. `--timeout` limits the total CLI operation and is not forwarded
to vLLM.

### Execution Model

```text
CLI host
  -> Endpoint and Cluster APIs
  -> matching Cluster credentials API
  -> selected inference runtime
  -> vllm bench serve through the Endpoint route
  -> CLI-host result directory
```

The CLI reads the Endpoint and referenced Cluster, validates eligibility, then
requests credentials for only that Cluster with `Client.GetClusterCredentials`.
Existing credentials API authorization is the permission boundary.

| Cluster type | Client runtime selection | Traffic route | Command transport |
|---|---|---|---|
| Kubernetes | First usable Running inference Pod by name; `vllm` container or the only container | `router-service.<namespace>.svc.cluster.local:8000/<workspace>/<endpoint>` | Kubernetes Pod exec |
| Static SSH | First Ray Serve `Backend` replica by node, replica, and Actor identity; map Actor PID to one Docker container | `<cluster-dashboard-host>:8000/<workspace>/<endpoint>` | SSH plus `docker exec` |

The Kubernetes tokenizer path comes from `vllm serve <model-path>` container
arguments. The static tokenizer path comes from the deployed Ray Serve
`model.path`. If either path cannot be determined, the command fails before
starting a case.

### vLLM Translation

For each case, the CLI runs the equivalent of:

```bash
vllm bench serve \
  --backend openai-chat \
  --base-url <Endpoint route> \
  --endpoint /v1/chat/completions \
  --model <routed model> \
  --dataset-name random \
  --random-input-len <input length> \
  --random-output-len <output length> \
  --num-prompts <num prompts> \
  --max-concurrency <concurrency> \
  --request-rate <request rate> \
  --save-result --save-detailed \
  --result-dir <runtime temporary directory> \
  --result-filename result.json \
  --metric-percentiles 50,90,95,99 \
  --percentile-metrics ttft,tpot,itl,e2el \
  --tokenizer <mounted tokenizer path> \
  --disable-tqdm
```

Before cases start, the CLI runs a `vllm bench serve --help` preflight and
queries `<Endpoint route>/v1/models` from the selected runtime. It selects the
first model's `id`, then `name`, then `model` field and passes it explicitly as
`--model`. This supports routers that return `name` instead of an OpenAI `id`
and prevents vLLM result-saving failures.

The tokenizer is not copied or downloaded. It is the mounted model directory
inside the runtime and is used by vLLM bench to create prompts with the
requested token lengths.

### Result Contract

Each invocation creates a directory under `--output-dir` on the CLI host:

```text
neutree-bench-<random>/
  manifest.json
  preflight.stdout.log
  preflight.stderr.log
  input-<input>-output-<output>-concurrency-<concurrency>/
    command.sh
    stdout.log
    stderr.log
    result.json
```

The target runtime uses a temporary `/tmp/neutree-bench-*` directory. The CLI
streams stdout and stderr through Pod exec or SSH/Docker exec. It then streams
`cat result.json` into the CLI-host artifact directory; it does not use `scp`
internally. Remote temporary directories are removed after transfer on a
best-effort basis. New run and case directories use mode `0700`; artifact files
use mode `0600`.

| File | Writer and timing | Contents |
|---|---|---|
| `manifest.json` | CLI; updated before execution and after every case state change | Run format and creation time; measurement scope; Endpoint workspace, name, and Cluster; selected runtime platform, ID, route, model, tokenizer, and model-selection mode; every case's input/output/concurrency, status, timestamps, and error. A top-level `error` is present when the run fails. |
| `preflight.stdout.log` | Selected runtime; once per invocation | `vllm bench serve --help` output, raw `<route>/v1/models` response, and the selected model line. |
| `preflight.stderr.log` | Selected runtime; once per invocation | Standard error from preflight and model discovery. |
| `command.sh` | CLI; once per case before execution | The exact POSIX-shell-quoted remote `vllm bench serve` command for the case, including its temporary result directory. It contains no API key, kubeconfig, or SSH private key. |
| `stdout.log` | vLLM bench; once per case | Raw standard output from vLLM, including its effective arguments, progress/status messages, warnings emitted to stdout, and final serving benchmark summary when the command completes. |
| `stderr.log` | vLLM bench; once per case | Raw standard error from vLLM, including diagnostics and tracebacks when a case fails. |
| `result.json` | vLLM bench, then streamed by the CLI; once per successful case | The unmodified `--save-result --save-detailed` JSON: benchmark identity and configuration, completed/failed counts, token totals and throughput, TTFT/TPOT/ITL/E2EL aggregate and percentile metrics, plus detailed per-request arrays such as lengths, timings, generated texts, and errors. |

`manifest.json` and logs remain available when a case fails. `result.json` is
created only after vLLM completes and the CLI can stream a non-empty remote
file, so it can be absent for a failed case. The CLI does not normalize the
vLLM result schema; detailed field names and optional fields remain owned by
the installed vLLM version.

## Scope and Non-Goals

The initial scope is vLLM, the `random` dataset, Kubernetes and static SSH
Clusters, and local artifacts. It does not add new API resources, persistence,
UI, historical comparison, scheduling, charting, capacity recommendations, or
new engine images.

Direct single-replica measurement is explicitly out of scope. It would measure
a different contract from the normal Endpoint route and needs a separate design.

## Security and Operational Behavior

- Operators supply the usual global `--server-url` and `--api-key` settings.
  A default Admin API key is required to read Cluster credentials, but the CLI
  does not add a special role check.
- Only the resolved Endpoint Cluster's credentials are requested. Kubeconfig
  and SSH private keys stay in memory or a restricted temporary SSH key file.
- Credentials do not enter benchmark commands, manifests, logs, or results.
- Artifacts can contain internal routes, runtime and tokenizer paths, vLLM
  configuration, and generated model text in detailed results. The CLI leaves
  them on the CLI host and does not upload or purge them automatically; the
  operator is responsible for output-directory access and retention.
- User-derived command values are validated or POSIX-shell quoted.
- The command generates live inference traffic. It is opt-in under `exp` and
  does not change Endpoint routing, Cluster state, database state, or images.

If validation, runtime selection, preflight, model discovery, a case, or result
transfer fails, the CLI preserves the available manifest and logs, stops later
cases, and cleans remote temporary files on a best-effort basis.

## Alternatives Considered

The alternatives come from four contract decisions: whether execution is manual
or CLI/control-plane owned, where the benchmark client runs, whether the target
is the Endpoint route or one replica, and whether results are local artifacts
or persistent platform state. The Kubernetes Router requirement and the
`neutree-cli exp bench` proposal select the CLI-owned, runtime-local,
Endpoint-route, local-artifact combination.

| Alternative | Rejected because |
|---|---|
| Ask operators to run vLLM bench manually | Repeats cluster-specific discovery and produces inconsistent commands and artifacts. |
| Run the client outside the target Cluster | Adds external gateway and network effects that do not represent the in-cluster serving path. |
| Benchmark one replica directly | Bypasses Router or Ray Serve behavior and changes what the result means. |
| Add a control-plane benchmark resource | Requires persistence, scheduling, retention, and UI before validating the basic CLI workflow. |

## Compatibility, Rollout, and Rollback

The command is additive and isolated under `exp`; existing CLI commands,
Endpoint schemas, routing, and engine images remain unchanged. Operators opt in
per invocation. Rollback is removal of the experimental command in a later
release; no persistent control-plane state is created.

The feature adds no control-plane metric, dashboard, alert, or network
endpoint. Per-case logs, `result.json`, and `manifest.json` are the operational
record for an invocation.

## Testing and Acceptance

### Unit Test

- CLI flags and early validation.
- Matrix expansion, vLLM command construction, model discovery, and artifact
  streaming.
- Kubernetes Pod selection and static Ray Actor, node, and Docker resolution.

### DB Test

Not applicable. The proposal adds no schema, migration, DB function, or RLS
change.

### E2E Test

Run a small random case against both a Kubernetes and a static SSH vLLM
Endpoint. Verify the expected route, manifest, logs, and `result.json` on the
CLI host.

### Manual Testing

Manual validation is needed only when the available E2E environment cannot
provide a vLLM-capable GPU Endpoint.

## Decisions Requested

Reviewers are asked to approve:

1. The experimental `neutree-cli exp bench` command surface and vLLM-only
   initial scope.
2. Endpoint-route measurement, rather than direct-replica measurement.
3. Runtime-local execution with CLI-host artifact collection.
4. `random` dataset matrices as the first benchmark contract.

## Decision Log

| Status | Decision | Resolution or revisit trigger |
|---|---|---|
| Proposed | Ship the command under `exp bench` | RFC reviewers approve the initial contract; promote only after it is stable across supported engines. |
| Confirmed | Pass model and tokenizer explicitly | Revisit if vLLM removes the local tokenizer requirement. |
| Deferred | Other engines, datasets, parallel execution, and history | Revisit after the CLI workflow is adopted. |

## Review Evidence

| Decision area | Evidence |
|---|---|
| CLI contract and matrix validation | `cmd/neutree-cli/app/cmd/bench/bench.go`, `internal/cli/bench/options.go` |
| Kubernetes and static execution | `internal/cli/bench/kubernetes.go`, `internal/cli/bench/static.go`, `internal/cli/bench/runner.go` |
| Model discovery and artifacts | `internal/cli/bench/model.go`, `internal/cli/bench/run.go` |
| Credential scope | `pkg/client/cluster.go` |
| Unit coverage | `cmd/neutree-cli/app/cmd/bench/bench_test.go`, `internal/cli/bench/*_test.go`, `pkg/client/cluster_test.go` |
