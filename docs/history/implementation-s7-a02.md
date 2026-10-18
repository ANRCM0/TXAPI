# S7-A02 Persistent Local Responses And Conversations

Date: 2026-10-18. Source base: `04fbfa62aad2ad6c3bbc2b4a153b634095c0b6a0`,
fetched latest master before starting a clean
`feat/s7-a02-persistent-conversations` branch. This is a working-batch record,
not matching remote CI/PR/merge acceptance. No A02/M2, full S7 or S5 checkbox
is closed by implementation or local results alone.

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
