# CLI Design

This document describes the design of `neutree-cli`, the command-line tool for managing Neutree resources.

## Design Principles

- **Generic resource operations**: `get`, `wait`, `apply` work with any resource kind, avoiding per-kind command explosion
- **Thin client**: CLI contains no business logic beyond formatting — all state lives server-side

## Command Structure

```
neutree-cli
├── apply       Create or update resources from YAML files
├── delete      Delete resources by name or from YAML files
├── get         Query resources (single, list, watch)
├── wait        Block until a resource meets a condition
├── launch      Deploy Neutree components (docker-compose)
├── model       Manage models
└── engine import  Import engine packages
```

`apply`, `delete`, `get`, and `wait` are the generic resource commands. The rest are purpose-built for specific workflows.

## Architecture

```
cmd/neutree-cli/app/cmd/
├── global/         Shared flags (--server-url, --api-key, --insecure) and NewClient()
├── apply/          apply command
├── delete/         delete command (with --wait mode)
├── get/            get command (with --watch mode)
└── wait/           wait command (with --for conditions)

pkg/client/
├── client.go       HTTP client with auth
├── resource.go     Low-level REST operations (list, get, create, update, delete)
└── generic.go      Kind-aware operations (ResolveKind, List, Get, Exists, Create, Update, Delete)

pkg/scheme/
└── scheme.go       Type registry, kind↔table mapping, ResolveKind
```

### Layering

```
CLI commands          Flags, args, output formatting
    ↓
global.NewClient()    Validate flags, create client with options
    ↓
GenericService        Kind resolution, workspace filtering, read/write routing
    ↓
resourceService       Raw HTTP: build URL, send request, decode response
```

Each layer has a single responsibility. CLI commands never construct HTTP requests directly.

## Generic Resource Model

All resource operations go through `GenericService`, which maps kind names to PostgREST table endpoints via the scheme registry.

### Kind Resolution

User input is resolved case-insensitively to a canonical kind:

| Input | Resolution Path | Result |
|-------|----------------|--------|
| `Endpoint` | Exact kind match | `Endpoint` |
| `endpoint` | Case-insensitive kind match | `Endpoint` |
| `endpoints` | Exact table name match | `Endpoint` |
| `Endpoints` | Case-insensitive table match | `Endpoint` |

### Read vs Write Paths

Write operations (`apply`, `delete`) are blocked for certain kinds (`ApiKey`, `UserProfile`) that are managed through other mechanisms. Read operations (`get`, `wait`) are allowed for all kinds.

### Raw JSON Design

Read operations return `json.RawMessage` instead of typed `scheme.Object`. This preserves the full server response (including `id` and other fields that the scheme decoder would strip) and enables direct JSON/YAML output without re-serialization.

## Command Details

### `apply`

```
neutree-cli apply -f resources.yaml [--force-update]
```

- Parses multi-document YAML, decodes via scheme into typed objects
- Sorts by dependency priority (Workspace → Cluster → Endpoint)
- For each resource: check existence → create or update (or skip)

### `delete`

```
neutree-cli delete <KIND> <NAME> [-w workspace] [--ignore-not-found] [--force] [--wait=true] [--timeout 5m]
neutree-cli delete -f resources.yaml [--ignore-not-found] [--force] [--wait=true] [--timeout 5m]
```

Two modes:
- **By name**: resolve kind, check existence via `Exists()`, call `Delete()` by ID
- **From file**: parse multi-doc YAML (same format as apply), sort in reverse dependency order (Endpoint → Cluster → Workspace), delete each resource sequentially

Deletion uses **soft delete**: `GenericService.Delete` first GETs the resource to retrieve full metadata, then PATCHes it with `metadata.deletion_timestamp` set. The full metadata backfill is needed because PostgREST replaces the entire composite field on PATCH. Controllers detect the timestamp and handle actual cleanup asynchronously (similar to Kubernetes finalizers).

`--force` sets the `neutree.ai/force-delete` annotation, which tells controllers to skip graceful shutdown.

By default `--wait=true`: after issuing the soft delete, the command polls `Get()` until the resource returns "not found" or the timeout expires. Output reflects the two-phase nature: `deleting` after PATCH, `deleted` after the resource is gone.

`--ignore-not-found` makes missing resources a no-op instead of an error.

### `get`

```
neutree-cli get <KIND> [NAME] [-w workspace] [-o table|json|yaml] [--watch]
```

Code structure follows a top-down call chain:

```
runGet → runOnce / runWatch
           ↓
     fetchResources        Unified: Get (by name) or List
           ↓
     resourcePrinter       Stateful: table header printed once across watch iterations
     ├── printJSON
     ├── printYAML
     └── printTable
```

`resourcePrinter` holds state so that `--watch` mode prints the table header only on the first iteration.

### `wait`

```
neutree-cli wait <KIND> <NAME> [-w workspace] --for <condition> [--timeout 5m] [--interval 5s]
```

Supported `--for` conditions:
- `delete` — wait for resource to not exist
- `jsonpath=.path=value` — wait for a JSON path to equal a value (uses gjson)

Code structure separates data fetching from condition matching:

```
runWait
├── parseForCondition → condition (interface)
└── poll()            Closure: Generic.Get + cond.match/matchNotFound
```

The `condition` interface has two methods:
- `match(data json.RawMessage) bool` — pure data matching, no I/O
- `matchNotFound() bool` — whether "not found" counts as a match (true for delete)

This keeps conditions testable as pure functions.

## Shared Patterns

### Client Creation

All commands use `global.NewClient()` which validates `--server-url` / `--api-key` and constructs the client with options. This eliminates repeated validation boilerplate.

### Error Handling

Commands set `SilenceUsage: true` and `SilenceErrors: true` on their cobra command. Errors from `RunE` are printed once by the root `Execute()` function, without appending the usage text.

### Output Formatting

Table output uses `text/tabwriter` with columns: `NAME  WORKSPACE  PHASE  AGE`. Phase is extracted via `ExtractPhase` (status.phase), age is computed from `metadata.creation_timestamp`.
