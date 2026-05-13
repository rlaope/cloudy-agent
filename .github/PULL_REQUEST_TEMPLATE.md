<!--
Thanks for the PR. Keep this template terse — anything you do not need can stay blank.
Project content (this PR body, commit messages, README/USAGE/CHANGELOG) is in English
and uses 평어 (-다 form for Korean prose). Do not add Co-Authored-By / Generated-with
trailers; sign commits with `-s` (DCO is required).
-->

## Summary

<!-- 1-3 sentences. What changes, and the why. -->

## Type of change

- [ ] Bug fix
- [ ] New tool / tool group
- [ ] Existing tool — new feature or schema change
- [ ] Refactor / internal cleanup
- [ ] Docs only
- [ ] Build / CI

## Read-only contract

<!-- For any change that adds or modifies a tool, confirm:
     - HTTP tools: only GET (transport guard remains in place)
     - SQL tools: only SELECT/SHOW/EXPLAIN/pg_stat_*/information_schema
     - Exec tools: fixed argv, no string concatenation from LLM input
     - bpftrace: only catalog additions, no free-form `program` arg
     - eBPF/perf tools: explicit platform/capability gate -->

## Test plan

- [ ] `go test -race -count=1 ./internal/...`
- [ ] `go vet ./...`
- [ ] `gofmt -l .` clean
- [ ] Manual smoke (note what you ran):

## CHANGELOG

<!-- Did you update CHANGELOG.md under `## Unreleased`? Tick or explain why not. -->

- [ ] Updated

## Related issues

<!-- "Closes #123" / "Refs #456" -->
