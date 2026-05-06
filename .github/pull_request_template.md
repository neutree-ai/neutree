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

- [ ] **Migration pairs** — every new `*.up.sql` ships with a matching `*.down.sql` ([database.md#migration-rules](../contributing/database.md#migration-rules))
- [ ] **Boundaries** — `scripts/check-boundaries.sh` clean (run `make install-hooks` to get this on every commit)
- [ ] **Docs** — `CLAUDE.md` / `contributing/` updated when conventions changed

## Risk & Rollback

<!-- DB schema change? Backwards-compat concerns? Rollback strategy? -->
