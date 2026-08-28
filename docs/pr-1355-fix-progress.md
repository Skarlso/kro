# PR #1355 — Fix progress tracker (living doc)

Source of truth for what's done / open. Verification detail lives in
`pr-1355-unresolved-full-verification.md`; this doc tracks execution.

**Scope:** the 74 unresolved review threads from the 2026-08-28 round
(cheeseandcereal). Legend: ✅ fixed+committed · ⚪ decided (no code / documented) ·
❎ false-positive · 🕐 awaiting-author-resolve on GitHub.

## Status summary

**Correction (audit outcome):** an earlier "all 74 resolved" claim was premature.
A line-by-line audit found 4 code findings that were still open; **all four are
now fixed** (`59b6b9ed`) with tests. Current honest state: **~70 fixed, 0 open
code findings, ~4 decided-no-code / reply-only.**

On GitHub, 73 threads are in the "unresolved" (unclicked) state by design — fix
replies are posted and left for the author/reviewer to click *Resolve*; only
`registry.go:135` was explicitly resolved (clean FP). 213 author replies posted.

**`make test WHAT=integration` is GREEN** (175/175 core + impersonation suite;
`6dfbb35e` fixed 3 stale assertions). `go build`, `make -n lint`, `helm template` pass.

## GENUINELY OPEN (audit found these uncommitted)

All four are now FIXED (`59b6b9ed`) and covered by tests; unit + integration suites green.

- ✅ `simple.go:778` — multi-GVR collection watch: `distinctWatchMappings` registers one
  selector watch per distinct GVR. +test.
- ✅ `simple.go:1344` — collection children stamped with instance-id (Graph UID) on the
  standalone path so the selector matches; scoped to COLLECTIONS (scalar co-ownership across
  Graphs must not conflict on the label — verified by Multi-Graph Isolation integration test,
  which initially regressed and drove the narrowing). +test.
- ✅ `simple.go:1407` — status-patch self-watch: `compiler.WithSelfWatchExempt` +
  `Node.SelfWatchExempt`; applyPatch skips the drift watch for the synthesized author-status
  writeback node (StatusPatchNodeID). +test.
- ✅ `graph_types.go:77` — `status.managedResources` MaxItems=5000 backstop (below etcd
  object-size limit; per-node forEach cap is the practical limiter). CRD regen.

## Decided / reply-only (no code, or premise inaccurate)

- ⚪ `graph/controller.go:149` teardown guards — COVERED BY INVARIANT: teardown uses the
  persisted AppliedServiceAccount, only ever written after a reconcile passed the
  self-impersonation + fail-closed guards (:237/:250), so a forbidden identity never
  reaches teardown. Re-running the guard is redundant. (reply)
- ⚪ `user-cluster-role.yaml:19` — file is NEW in this PR (`730df93d`), so the reviewer's
  "users lose PREVIOUS aggregated access on upgrade" premise is inaccurate; the
  no-instance-access design point is minor. (reply)
- ⚪ `cmd/controller/main.go:344` (example RGD-as-Graph dual-ownership) &
  `graphengine.go:56` (duplicate watch router) — architecture/design notes; need a
  maintainer call (reply or descope), not a mechanical fix.
- ⚪ `graph/controller.go:306` managedResources double-write — PARTIAL (growth-gated
  write-ahead already limits churn); reply.

## Commits (chronological, by batch)

- **Batch 1** `b3f3e0a7` — cache TOCTOU (cached.go:128), forEach data-pending (node.go:448),
  coordinator empty-old-labels (coordinator.go:199), cost-limit threading (status.go:396).
- **Batch 2** `2559b399` — executor Apply/Delete hard-error aggregation (272/493),
  malformed-create fail-fast (751), update-rejection log.
- **Batch 5** `5f8c171b`,`7ce66a90`,`803597d4` — compiler/parser (compiler.go:651/730,
  typecheck.go:302/325, validation.go:206, cel.go:31, parser.go:285).
- **Batch 6** `4f0c2777`,`4b0ff871`,`5d6fd30b`,`a4a87dda` — schema-wrapper typing (status.go:205),
  lossless schema events (watcher.go:457), scope-leak (runtime.go:108), soft-dep list seed
  (runtime.go:178), dead projector removal (status.go:66), cache metrics (cached.go:104).
- **Batch 7 (agent)** `814e91d2`,`f64591e8`,`2252bb81` — spec Required+CRD regen (graph_types.go:258),
  lint cmd/kro (.golangci.yml:98), helm --graph-concurrent-reconciles (deployment.yaml:188).
  `1ceb79bf`,`d9b0ca5d` — graph.md doc accuracy + nested compile test.
- **Batch 3 (lifecycle)** `65d14946` (instance 266/663), `3d6bc14b`+`0ee2b54b` (graph 421/357),
  `7a3ffb47` (release 1489/1492), `025382e6` (impersonation LRU 129), `f0004a3c` (pre-write
  duplicate-identity 207/1627), `51c90831` (contribution write-ahead 314), `f50ab83b`
  (Group+Kind identity, tracking.go:34), `62d570da` (subgraph write-ahead recursion 137),
  `767242b2` (dynamic-ns churn 161).
- **main.go:350** `d8d3d876` — fail-closed when GraphKind on but controller identity incomplete.
- **Decisions batch** `430685c4` (#5 helm impersonate gate), `f08df2e8`+`973f2248` (#4 contribution
  inventory: Graph→status + instance FM-guard), `f274f34e` (#2 apply-order doc), `4e2a5955` (#1
  886 Opt 2: converge+event), `13122c53` (#6 examples future-work flags).
- **Integration fixups** `6dfbb35e` — stale test assertions (compilation msg + patch status.Contributions).

## The six maintainer decisions (all closed)

| # | Finding | Decision | Commit |
| --- | --------- | ---------- | -------- |
| 1 | `886` update-rejection | Opt 2 — converge + `UpdateRejected` Warning event (no wedge) | `4e2a5955` |
| 2 | `runtime.go:203` apply-order | Opt A — documented revision-local; deletion already tolerant | `f274f34e` |
| 3 | `cached.go:91` epoch growth | Opt A — documented bounded (reclamation reintroduces TOCTOU) | `a4a87dda` |
| 4 | `deletion.go:139` release trust | Graph→`status.Contributions`; instance FM-prefix guard | `f08df2e8`,`973f2248` |
| 5 | `cluster-role.yaml:155` impersonate | gate behind GraphKind | `430685c4` |
| 6 | examples | keep in-tree, flag aspirational parts as FUTURE WORK | `13122c53` |

## False-positive / decided-no-code

- ❎ `registry.go:135` — race genuinely closed (presence + monotonic counter). **Resolved on GitHub.**
- ⚪ `.golangci.yml:43` — cited doc DOES exist (replied); staticcheck-exclusion scoping is a
  maintainer preference, left as a reply.
- ⚪ `compiler/context.go:237/256` — deferred-schema / CRD-CEL-in-spec is a **product/KREP decision**
  (deferred-schema-resolution proposal), not a code fix. Out of scope; replied.
- ⚪ `examples_test.go:127` — permissive test-resolver schema accepts unknown Kinds; inherent to the
  fake resolver, informational.
- ⚪ `examples_test.go:159` — nested compile coverage: inline-`graph:` covered (`d9b0ca5d`);
  runtime-stamped children are integration-level (documented, not compiler-reachable).

## Items resolved that I re-verified during the audit

- ✅ `rgdadapter/status.go:396` — author-condition compile threads `costLimit` through
  `compileCEL` (verified: `ProgramOptions(costLimit)`, not DefaultProgramOptions).
- ✅ `simple.go:272/493/751/886/1489/1492/1627` — all committed (batches 2/3).
- ✅ `tracking.go:34/137/161` — all committed (batch 3).
