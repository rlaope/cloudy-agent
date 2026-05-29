---
name: release-sync
description: >-
  Cut a cloudy release and keep the local install in sync. Use when
  promoting the Unreleased changelog to a version, tagging, building with
  the canonical ldflags, or rebuilding/reinstalling the local cloudy
  binary after a merge. Encodes cloudy's exact build + version stamping.
---

# release-sync

cloudy stamps its version from `git describe` into
`internal/buildinfo.Version` at build time. This skill cuts a release and
keeps the operator's local binary current.

## Canonical build (matches CLAUDE.md + Makefile)

```bash
go build -trimpath \
  -ldflags "-s -w -X github.com/rlaope/cloudy/internal/buildinfo.Version=$(git describe --tags --always --dirty)" \
  -o ~/.local/bin/cloudy ./cmd
```

`make build` does the same but outputs `./cloudy`; the `~/.local/bin/cloudy`
path is where the user QAs from. CI runs `go test ./...` and
`golangci-lint v2.12 run --timeout=5m ./...` (`go.mod` targets go 1.25.0;
lint is v2 with `staticcheck` restricted to `SA*`).

## Cut a release

1. **Promote the changelog.** Rename `## vX.Y.Z — Unreleased` to the real
   version, confirm every shipped change is listed (delegate gaps to
   `docs-sync`), open a fresh `Unreleased` section above it.
2. **Green check.** `go build ./...`, `go test ./...`,
   `golangci-lint run --timeout=5m ./...` all clean.
3. **Tag.** `git tag vX.Y.Z && git push origin vX.Y.Z` — the tag is what
   `git describe` stamps into the binary version.
4. **Build + reinstall** with the canonical command above.

## Sync local install after a merge (the common case)

After a PR merges, refresh the operator's binary:

```bash
git checkout master && git pull --ff-only origin master
VER=$(git describe --tags --always --dirty)
go build -trimpath -ldflags "-s -w -X github.com/rlaope/cloudy/internal/buildinfo.Version=$VER" -o ~/.local/bin/cloudy ./cmd
~/.local/bin/cloudy --version   # confirm the new version stamped
```

## Conventions

- Conventional commits in English; DCO sign-off (`git commit -s`) required;
  no `Co-Authored-By: Claude`.
- Default flow: feature branch → `gh pr create` → `/code-review` → apply
  findings → `gh pr merge --merge --delete-branch` → sync master →
  rebuild → reinstall. Direct master pushes only when explicitly told.
