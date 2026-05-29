---
name: docs-sync
description: >-
  Keep cloudy's docs in sync with the code after a change. Use when tool
  counts, feature lists, config fields, or runtime invariants drift from
  what README.md / CHANGELOG.md / docs/*.md / CLAUDE.md claim. Docs only —
  never edits Go.
---

# docs-sync

cloudy's docs make concrete claims (tool counts like "45 → 69", group
counts, feature bullets, config fields). When code changes, these drift.
This skill detects and fixes the drift. It touches docs only.

## Doc surfaces

| File | Holds |
|------|-------|
| `README.md` | User-facing feature claims, tool/group counts, quickstart |
| `CHANGELOG.md` | `## vX.Y.Z — Unreleased` section, grouped by Added/Changed/Fixed, PR refs (`(#NN)`) |
| `docs/*.md` | `AUTO_DISCOVERY`, `BASTION`, `PERMISSION_PROFILES`, `SAFETY`, `RFC-RAG` deep-dives |
| `CLAUDE.md` | Runtime invariants + conventions (keep in sync when an invariant changes) |
| `CONTRIBUTING.md` | Contributor workflow |

## Procedure

1. **Diff intent.** Read the change (PR diff or working tree). Identify
   what user-visible facts moved: new/removed tools or groups, new config
   fields, changed defaults, new runtime invariant.
2. **CHANGELOG first.** Add bullets under the `Unreleased` section in the
   matching `Added` / `Changed` / `Fixed` group, with the PR number
   `(#NN)`. Match the existing terse, technical bullet style.
3. **README counts.** If tool/group totals changed, update the count
   claims so they stay truthful (grep for the current numbers first).
4. **Deep-dive docs.** If the change touches discovery, bastion,
   permissions, or safety behaviour, update the relevant `docs/*.md`.
5. **CLAUDE.md invariants.** If the change adds or alters a runtime
   invariant (wire format, skip routing, redaction seam), reflect it in
   the "Runtime invariants worth knowing" section so future work doesn't
   re-break it.
6. **Verify:** docs only — no build needed. Re-grep the numbers/claims you
   edited to confirm there's no stale copy left elsewhere.
7. **Commit:** `git commit -s -m "docs: ..."` then the standard PR flow.
