# S7-A02 Persistent Local Responses And Conversations

Date: 2026-10-18. Source base: `04fbfa62aad2ad6c3bbc2b4a153b634095c0b6a0`,
fetched latest master before starting a clean
`feat/s7-a02-persistent-conversations` branch. Earlier checkpoints below retain
their source-specific pending status. Final A02/M2 acceptance through PR #12
is recorded in [functional acceptance](#2026-10-18-s7-a02-functional-acceptance).
Full S7 and S5 remain incomplete; local results alone do not close them.

## Scope And Ownership

- `internal/responses`: actual encrypted history application, HTTP CRUD/list,
  full local-context continuation, bounded output observation and revision/unknown
  behavior. Consumes small Store/Executor ports, never imports GORM/store/billing.
- `internal/model` / `internal/store`: v12 history structure, current creating
  user/Key/model authorization, writer-lock/CAS, count/byte reservations, bounded
  cleanup and startup uncertainty. No independent migration runner or ledger.
- Gateway: application-owned original-target continuity constraint is checked
  inside ordinary authorization/capability/routing/admission/attempt processing.
  Every generation still uses the existing coordinator and financial finalization.
- Integration: main/config/key requirements/HTTP/CORS/OpenAPI are coordinated
  together; OpenAPI working version 0.4.0 / 91 operations, 11 new history routes.
  Delivery/migration tests retain prior identities, prices, financial and rollback
  assertions while extending their expected schema to v12.
- No production database/account/key, background worker or WS product was used.
  Existing frontend consumes unchanged management contracts; history UI remains A06.

The exact behavior, defaults, local/provider state mapping, deletion/retention
and unsupported options are in [the history contract](../reference/responses-history.md).
Official documentation was fetched before implementing its endpoint shapes.

## Development And Concentrated Verification

Implementation and risk regressions were authored in a batch, with `GOARCH=amd64`
`go build ./...` used only as an implementation compile check. No tests were
deleted, no dependencies introduced and no CI trigger/gate changed.

Local environment: Windows/PowerShell, Go 1.26.3 with **explicit GOARCH=amd64**
for every Go command; temporary SQLite, fake native executors and real loopback
HTTP. Browser checks use existing installed Node/Playwright/Chromium paths and
isolated temporary production binaries, not real providers.

First concentrated `go test -json -count=1 ./...` passed 1012 tests/subtests,
24 packages, five expected subprocess-only helper skips, no failures. That run
preceded final target-reload/wire-ID and deletion-after-receipt-expiry boundary
repairs, and is not evidence for later source. A focused subsequent run of
responses/store/server/gateway/main packages passed on its then-current source.
The final frozen executable/configuration/contract/test source is 295 non-Markdown
paths with fingerprint `3e9124d68b8adae8eff3679445ff4beed5594e53bcdde6c118d8db50f13ac45a`
(SHA-256 of the sorted path-to-content-SHA-256 compact JSON map).

The final frozen fingerprint matched before and after the complete suite and
after all later Markdown edits. Final actual results are recorded below; the
first source's green result and historical A01/S6 CI do not certify this source.

| Final command / scope | Actual result |
|---|---|
| `GOARCH=amd64 go test -json -count=1 ./...` | 1015 tests/subtests and 24 packages passed; five expected subprocess-only helper skips, no failures |
| `GOARCH=amd64 go build ./...`; `go vet ./...` | Both passed |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | Passed, no dependency drift |
| Changed-Go normalized-LF gofmt comparison | 28 changed/new Go files passed; no unrelated Go formatting |
| `node --check web/assets/app.mjs`; `node --check web/assets/api.mjs`; `node --test web/assets/api.test.mjs` | Syntax passed; 10 API-client behavior cases passed |
| `GOARCH=amd64 python tests/smoke/local.py --go go` | Passed: existing actual main/auth/Chat/SSE/console, strict refusal, forced-kill pending/waiver, encryption/runtime restart and shutdown |
| `GOARCH=amd64 python tests/smoke/compatibility.py --go go` with existing NODE_PATH / Chromium paths | Passed: temporary production binary/SQLite with real API/UI saves, publication, restricted Key/model, playground, logs, role isolation and desktop/mobile; cleanup passed |
| `tests/smoke/delivery.py` AST parse | Passed; actual container execution is still pending CI, not simulated by parsing |
| `git diff --check`; changed-Markdown local links/anchors and rule consistency | Passed; see final local-delivery note for count |

The compatibility browser workflow checks unchanged existing console consumers,
not an A02 history UI or SDK/Codex acceptance. No browser dependency was installed.
No test failure occurred in the recorded full/focused/final runs. Review-driven
repairs retained the source/CI boundaries: deleting history after its receipt
expired must remain possible; local synthetic item IDs must not be forwarded
as provider IDs; target reload checks use real configuration publication; storage
errors are distinguished from CAS conflicts, and capacity-failed paired writes
roll back atomically. The final suite supersedes earlier local results.

No Go/consumer source changed after final validation; later status/document
updates are Markdown only and do not reuse historical CI as their own validation.

## Requirement And Regression Map

| Requirement | Implementation / consumer evidence |
|---|---|
| Response GET/delete/input items and Conversation/item APIs | 11 authenticated real HTTP routes, OpenAPI/anonymous-auth contract gates; application tests cover metadata, pagination, item read/delete, stale ETag and deletion/replay |
| Parent/context/target/price provenance | Encrypted effective request/input/output and parent/conversation snapshot; receipt keeps native upstream identity; target fingerprint and captured price version belong to each new generation |
| Exactly one context source | Full local expansion removes both upstream reference fields; tool-call IDs/opaque reasoning survive; actual native JSON/SSE server regression checks full prior/new context and one new legacy charge per generation |
| Owner/Key/model authority | Current Store transaction checks and each delivery resolver; same-user other-Key and cross-owner IDs return 404, revoked model cannot read, no admin override |
| Encrypted bounded storage | AEAD metadata binding, retained-key requirements, v12 migration, fixed TTL, bytes/count reservations, store:false no history, tamper/expiry/capacity and multi-record rollback regressions |
| Restart, branching and concurrency | Real SQLite close/reopen with old-key retention/new active key; independent sibling snapshots, concurrent turn conflict, atomic CAS writers, interrupted/unknown slots blocked, original target/key/mapping changes rejected |

The gateway target guard narrows an already authorized candidate set; it does not
grant routing authority or bypass current policy. Responses strict permission is
unchanged: all strict Responses are refused before billing/admission/attempt.
The private A01 cache is not used as durable conversation authority.

## Outstanding Acceptance

At the final local checkpoint before submission, implementation and the final
concentrated outcomes above were complete on the frozen worktree; no A02
commit/push/PR/merge had occurred. The following integration requirements remain
pending until the actual submission and authoritative CI/merge verification:
matching automatic push/PR and merged-master complete CI,
Linux full race, v12 container fresh/upgrade/stopped-backup restoration and
latest-base integration remain outstanding until a real submission/acceptance.
No functional acceptance checkbox is inferred from this document. A03-A06,
M2/full S7 completion, real vendor/SDK/Codex and S5-03 remain separate.

## Final Local Documentation Check

Windows/PowerShell `git diff --check` and the ignored local
`python .local/validation/check-foundation-docs.py` passed for ten changed
Markdown documents and 175 local links/anchors. Rule/status review retained
all A02/M2/S5 checkboxes and distinguished historical A01 acceptance from
the new source. The final non-Markdown fingerprint remained unchanged.
No temporary logs, databases, keys or helper scripts are part of the batch.

## Post-Submission Reconstruction And Ordering Repair

Initial feature head `a760269ee700fc56d49ca2a91fa3c0b750f399cb` was pushed
from unchanged latest base `04fbfa6` and submitted as PR #12. Its source has the
1015-test Windows result above; it does not certify the subsequent repair.
Automatic initial runs are push 39091271249 and PR 39091331439. Their actual
conclusions are recorded only after reading the completed runs.

Final review found a missing reconstruction boundary: a native reasoning item
without encrypted content may rely on upstream hidden state. The HTTP validator
now refuses caller-supplied ID-only reasoning (including store:false); the local
history application retains native output unchanged but refuses continuation
with 409 history_context_unavailable when its full input cannot be reconstructed.
It never drops the reasoning or authorizes a provider lookup by a guessed item ID.
Encrypted-content round trips remain unchanged. The new regression proves GET
preserves the item and continuation/native ID-only input causes zero new calls.

Pagination ordering was also made explicit: list default and OpenAPI now use
desc, the existing ascending-pagination regression requests asc, and a default
order assertion was added. Search-index text and fetched endpoint/typed pages
differed in whether they exposed order; the actual fetched pages only list
after/limit, so the local order extension is documented explicitly without
claiming complete vendor/SDK certification. Official reasoning/typed-list pages
were fetched and read on 2026-10-18. Neither repair changes financial authority,
retention, migration version, existing CI/dependencies or frontend contracts.
Final exact-source local/CI results supersede the earlier source-specific entries.

The initial a760269 push 39091271249 and PR 39091331439 both completed
check SUCCESS / delivery SUCCESS (read from their exact head). Those runs
remain evidence for the initial source only; they do not authorize merging
the later reasoning/order repair without matching new complete CI.

### Final Repaired Local Source

The repaired frozen source contains 296 non-Markdown paths, SHA-256
`41d33e323f7741e7146d3030be890e7ecb4fdb74e7bde1ea3e96fb97537512ca`,
matched before/after the final suite and after subsequent documentation changes.
On Windows/PowerShell with GOARCH=amd64, `go test -json -count=1 ./...`
passed **1016 tests/subtests, 24 packages**, five expected helper skips and
no failures. `go build ./...`, `go vet ./...`, `go mod tidy`, dependency diff,
31 changed-Go normalized-LF gofmt comparison and delivery.py AST parse passed.
The repaired `python tests/smoke/local.py --go go` again passed the real main
startup/auth/normal/SSE/console/strict refusal, forced-kill recovery, encrypted
credentials/runtime and shutdown scenarios. Focused native protocol/application/
gateway/server repair tests also passed before that final full suite.

The earlier ten API-client cases and real compatibility.py API/UI desktop/mobile
workflow on initial head a760269 are reused for their unchanged frontend/Chat/
management scope: the repair only changes native reasoning admission, local
history reconstruction errors and list ordering. Native protocol/application/
HTTP behavior was instead reverified on the repaired source, not inferred
from the prior browser result. No dependency, workflow, financial or migration
behavior changed in this repair. Matching new automatic push/PR/master complete
CI remains mandatory before final functional acceptance.

### Authenticated Unknown Marker Repair

A further boundary review found that a completed uncertain conversation had
its blocker only in the plaintext busy column. The repair keeps an uncertain
marker in authenticated ciphertext too. A regression clears the database busy
column and proves repeated new keys still return 409 with no new execution.
Success and proven never-dispatched paths explicitly clear the encrypted marker.
The two changed source files affect private history state only; no route/schema,
financial, migration, protocol wire, frontend or lifecycle interface changed.

Final current source: 296 non-Markdown paths, fingerprint
`7bf33379b9ddcccceab59c0a075b03e0333415a387f734aebb7268931b627fd7`.
`GOARCH=amd64 go test -count=1 ./internal/responses ./internal/server ./internal/store`
passed all three affected packages, including the new actual tamper/re-execution
regression; `go build ./...` and `go vet ./...` passed again. The complete local
1016-test result remains explicitly scoped to prior head 8ca8901 / fingerprint
41d33e32, reused only for unchanged packages/behavior. Its full push/PR CI is
also source-specific. The final current head must pass the existing complete
automatic Linux race/main smoke/container jobs before merge; no gate or source
change is inferred from earlier green results.

## 2026-10-18 S7-A02 Functional Acceptance

Final feature head `2084bcddf3d39d7f62f65ff138072431427fea43` was integrated
on unchanged latest base `04fbfa62aad2ad6c3bbc2b4a153b634095c0b6a0` through
[PR #12](https://github.com/ANRCM0/TXAPI/pull/12), merged at
2026-10-18T03:09:06Z as `master/765501a93f22c6daf125aa98b718fd74dd316abf`.
The merged tree exactly matched the feature head (`git diff --exit-code`
between them passed). Its 296 non-Markdown source paths retain fingerprint
`7bf33379b9ddcccceab59c0a075b03e0333415a387f734aebb7268931b627fd7`.

| Exact source / automatic run | Actual result |
|---|---|
| Final feature push [39092263704](https://github.com/ANRCM0/TXAPI/actions/runs/39092263704), `2084bcd` | check SUCCESS / delivery SUCCESS |
| Final feature PR [39092266694](https://github.com/ANRCM0/TXAPI/actions/runs/39092266694), `2084bcd` | check SUCCESS / delivery SUCCESS |
| Merged master [39092604090](https://github.com/ANRCM0/TXAPI/actions/runs/39092604090), `765501a` | check SUCCESS / delivery SUCCESS |

Authoritative results were read with `gh run view --json headSha,status,conclusion,jobs`;
head/base and mergeability were checked with `git fetch origin master`, `git rev-parse`
and `gh pr view`. Merge used `gh pr merge 12 --merge --match-head-commit` with
the exact final feature SHA after all four push/PR checks succeeded. No manual
dispatch, workflow modification, gate bypass or administrator override was used.

All three existing Verify runs completed dependency/contract checks, build,
vet, gofmt, frontend checks, **Linux full race**, coverage, real-main loopback
smoke and report-only performance baseline. Delivery ran the actual Docker/Compose
fresh install, S3-to-v12 upgrade, stopped consistent backup/restoration and normal
shutdown, preserving old identity/price/financial assertions. These are real CI
outcomes, superseding the earlier AST-only/pending delivery checkpoints for the
final source. Local 1016-test results remain scoped to `8ca8901`; final `2084bcd`
additionally passed affected-package tests and these exact-source full CI runs.

The six A02 requirements and M2 (A01 plus A02) are accepted: stored response/
conversation/items operations, one complete local context source, target/price
provenance, creating user/Key/current-model authority, bounded encrypted storage,
restart/branch/CAS/deletion/expiry and uncertainty behavior. No new financial
authority, authentication system or migration runner was introduced.

Acceptance remains the declared single-model native text/function/opaque-encrypted
subset. Missing reasoning content refuses continuation before execution;
unknown execution remains blocked even after the plaintext busy field is cleared.
Responses stays disabled by default and requires actual credential encryption and
mounted content keys. All strict Responses remain refused before billing.
No real vendor account, production database, SDK/Codex or new history UI was tested.
A03 background, A04 WS, A05/A06 reliability/client delivery, full S7 and S5-03
are still incomplete. The next independent batch is A03.

The subsequent acceptance documentation batch changes Markdown only. It checks
diff, links/anchors and state consistency without repeating Go tests or manually
dispatching CI; its ordinary automatic push/PR/master integration remains separate
from the functional evidence above.

Windows/PowerShell `git diff --check` and the ignored local
`python .local/validation/check-foundation-docs.py` passed for ten changed
Markdown documents and 180 local links/anchors. State review confirms only the
six A02 checkboxes changed; A03-A06, full S7 and S5-03 remain incomplete.
The non-Markdown source fingerprint above was unchanged after the documentation
edits. No new local Go/browser test or manual CI dispatch was required.
