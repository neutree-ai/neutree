## Issue

<!-- Linear / Jira / GitHub issue link, or "n/a" -->

## Summary

<!-- High-level intent. Why is this change needed? One paragraph, no diff narration. -->

## Changes

<!-- Bulleted list of meaningful changes. -->

## Test Plan

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] E2E (if affected): paste the command + label, otherwise "not affected"
- [ ] DB integration (`make db-test`) ran, otherwise "not affected"

## Constraints Checklist

Mark "n/a" for items that do not apply. References live under [`contributing/`](../contributing/README.md).

- [ ] **Dual-path** — Kubernetes and SSH/Ray orchestration paths both reviewed ([invariants.md#dual-orchestration-path](../contributing/invariants.md#dual-orchestration-path))
- [ ] **Migration pairs** — every new `*.up.sql` ships with a matching `*.down.sql` ([database.md#migration-rules](../contributing/database.md#migration-rules))
- [ ] **Test co-change** — implementation and `_test.go` updated in the same commit ([invariants.md#implementation-and-test-pairs](../contributing/invariants.md#implementation-and-test-pairs))
- [ ] **Boundaries** — `scripts/check-boundaries.sh` clean (run `make install-hooks` to get this on every commit)
- [ ] **Docs** — `CLAUDE.md` / `contributing/` updated when conventions changed

## Risk & Rollback

<!-- DB schema change? Backwards-compat concerns? Rollback strategy? -->
