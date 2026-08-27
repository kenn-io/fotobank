# Fotobank on Docbank Milestone 1 Plan Index

**Goal:** Deliver Milestone 1 as independently reviewable Docbank and Fotobank
pull requests without designing later work against APIs that have not landed.

**Architecture:** The master design controls cross-cutting decisions. Each
pull request gets its own executable Superpowers plan when its direct inputs
exist. This file records ordering, plan availability, dependency gates, and the
post-merge kata setup; it is not a second implementation plan.

**Spec:**
[`docs/superpowers/specs/2026-08-25-fotobank-docbank-master-design.md`](../specs/2026-08-25-fotobank-docbank-master-design.md)

## Planning rule

Plans are written just in time from the exact target-repository baseline and
pinned dependency signatures available when their pull request can start. An
absent later plan is deliberate, not missing work.

- D02, F01, and F02a can be planned now because their inputs exist.
- F03 is planned after D02 and F02a land. Its consumer cutover uses the merged
  final-shaped asset repository, and its adapter calls use D02's exact pinned
  Go pseudo-version and public signatures.
- F04 and F05 are planned after F03 lands. They use the actual content adapter,
  operation ledger, and asset/file repository that F03 establishes.

Every executable plan starts with the required Superpowers header, names its
exact base, lists created and modified files, defines produced interfaces, uses
test-first steps, and ends at a pull-request handoff. Executors use
`superpowers:using-git-worktrees` before implementation and then either
`superpowers:subagent-driven-development` or `superpowers:executing-plans` as
the plan directs.

## Pull-request graph

```text
D02 ───────────────┐
                   ▼
F01 → F02a ──────→ F03 → F04
                   └────→ F05
```

| PR | Target | Executable plan | Plan timing |
|---|---|---|---|
| D02 | Docbank | [Exact-version logical ranges](2026-08-26-docbank-d02-version-ranges.md) | Ready |
| F01 | Fotobank | [Embedded vault boundary](2026-08-26-fotobank-f01-embedded-vault.md) | Ready |
| F02a | Fotobank | [Final-shaped asset/file domain](2026-08-26-fotobank-f02a-asset-domain.md) | Ready |
| F03 | Fotobank | Not yet authored | Ready: D02 and F02a are merged |
| F04 | Fotobank | Not yet authored | After F03 merges |
| F05 | Fotobank | Not yet authored | After F03 merges |

The D02 plan lives here because Docbank's repository policy makes kata, not
documentation, its implementation tracker. D02's Docbank kata issue and pull
request description must stand alone for a Docbank reviewer. They link to the
master design for cross-project context but do not require the reviewer to
reconstruct the request from Fotobank commits.

## Pinned planning baselines

- Fotobank design baseline: `25f37a194781efc63372f6dd7f4395c79d2a491f`.
- Fotobank planning pull-request base: `origin/main` at
  `b8a9dc35f00a3e07f72d23eaae924d870aa859a8`.
- Docbank D02 source baseline: `origin/main` at
  `32a91309ae43b344039909220160646868689d78`.
- F01 consumes released Docbank `v0.14.0` at
  `41a0fbba06f173aa0690505d16584addb58cff5d`; its required public embedded
  surface matches the D02 planning baseline.
- Fotobank pins D02 commit `db49081eed887228d57cec1af3d175e2e0f2c8dd`
  as Go pseudo-version `v0.14.1-0.20260826164655-db49081eed88`. This exact
  merged development revision is the F03 planning and implementation baseline.
- Docbank at the D02 baseline consumes `go.kenn.io/kit v0.17.1`.

Each later plan replaces these planning-time facts with the exact merged base
and exact pinned Docbank version present when that plan is written. During the
alpha integration, a merged commit expressed as a Go pseudo-version is the
normal dependency boundary: never use a floating branch, local replacement,
or unmerged revision. After the Milestone 1 gate demonstrates that the public
surface is sufficient, Docbank tags the accepted revision and Fotobank moves
to that tag before real data is entrusted to the system.

## Operational transition

The five governing design commits originally existed only on local `main`.
Before new planning edits began, the transition performed these guarded steps:

```bash
git switch -c docs/docbank-rebuild-planning \
  25f37a194781efc63372f6dd7f4395c79d2a491f
git branch -f main origin/main
```

The feature branch retained all five commits before local `main` moved. The
branch pointer was verified at `25f37a1`, and local `main` was verified equal to
`origin/main` at `b8a9dc3`; no commit or file became unreachable. The planning
pull request also changes `CLAUDE.md` (and therefore the `AGENTS.md` symlink) so
future sessions use feature branches, isolated worktrees where appropriate,
agent-opened pull requests, and user-controlled merges.

## Kata setup after the planning PR merges

Do not create milestone issues from an unmerged design. After this planning
pull request lands, search both ledgers before creating anything:

```bash
kata search "Docbank embedded exact version ranges" --project docbank --agent
kata search "Fotobank Docbank milestone 1" --project fotobank --agent
kata search "embedded Docbank vault" --project fotobank --agent
kata search "asset file domain" --project fotobank --agent
```

If no matching issues exist, create the Docbank issue in Docbank's project:

```bash
kata create --project docbank \
  "D02: expose embedded exact-version logical ranges" \
  --body "Add catalog-authorized exact-version logical byte ranges required by Fotobank video delivery. The pull request must stand alone and link the Fotobank Docbank master design for cross-project context." \
  --label feature --label cross-project \
  --idempotency-key docbank-d02-embedded-version-ranges \
  --agent
```

Record the returned ref as `docbank#<d02-ref>`. Then create the Fotobank
milestone parent and children, substituting the refs returned by the preceding
commands:

```bash
kata create --project fotobank \
  "Milestone 1: Docbank content substrate and asset domain" \
  --body "Deliver D02 plus F01, F02a, and F03 through F05, then pass the representative import, restart-recovery, verified-photo, and video-range gate in the master design." \
  --label epic --label backend \
  --idempotency-key fotobank-docbank-m1 \
  --agent

kata create --project fotobank "F01: embed the Docbank vault" \
  --body "Add the released Docbank module, configuration and lifecycle, and Fotobank's sole internal Docbank adapter. No product content path changes in this pull request." \
  --parent <m1-ref> --label backend \
  --idempotency-key fotobank-docbank-f01 --agent

kata create --project fotobank "F02a: add the asset and file domain" \
  --body "Add the opaque asset, media-file, relationship, cached Docbank mapping, and stable owner-storage-key schema and repository without a second product write path." \
  --parent <m1-ref> --blocked-by <f01-ref> --label backend \
  --idempotency-key fotobank-docbank-f02a --agent

kata create --project fotobank "F03: make Docbank original authority" \
  --body "Atomically move active consumers and foreign keys to assets, grouped imports and all original reads to exact pinned Docbank APIs; reject canonical or symlink-aliased import-source/vault overlap before discovery; and remove the old media schema, storage path, and MD5 identity without a compatibility bridge." \
  --parent <m1-ref> --blocked-by <f02a-ref> \
  --blocked-by docbank#<d02-ref> --label backend \
  --idempotency-key fotobank-docbank-f03 --agent

kata create --project fotobank "F04: resolve exact content versions" \
  --body "Add the shared asset, file, and exact-version resolver used by current reads, historical reads, and later checkout rebuilds." \
  --parent <m1-ref> --blocked-by <f03-ref> --label backend \
  --idempotency-key fotobank-docbank-f04 --agent

kata create --project fotobank "F05: recover interrupted imports" \
  --body "Recover pending create operations across process restarts and report unmatched nodes under each owner's Docbank subtree without deleting authority." \
  --parent <m1-ref> --blocked-by <f03-ref> --label backend \
  --idempotency-key fotobank-docbank-f05 --agent
```

Use `kata show <ref> --agent` on every created issue and verify its project,
parent, and blockers. The resulting edges must be:

```text
F01 blocks F02a
F02a and docbank#D02 block F03
F03 blocks F04 and F05
```

Do not create F03, F04, or F05 plan documents merely because their kata
issues exist. Their issue bodies preserve approved scope; their executable
plans still wait for the exact dependency baselines described above.

## Milestone gate

Milestone 1 completes only after a fresh deployment:

- imports a representative RAW/JPEG/XMP group and a video into Docbank;
- restarts at every injected operation boundary without duplicate history;
- serves verified full photo bytes and correct video byte ranges;
- exposes only opaque Fotobank asset identifiers through product routes; and
- contains no legacy original-byte storage path, MD5 identity, fallback read,
  dual write, or compatibility alias.

The F05 plan owns the executable end-to-end gate after F03 has established the
actual adapter, ledger, and repository contracts.
