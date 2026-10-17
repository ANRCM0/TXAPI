# S7-A01: Native Responses HTTP/SSE Batch

Date: 2026-10-18. Branch: `feat/s7-a01-responses-http`; source integration base: `bea97faefce65fc5ecf74b8703690c71b2f8cfe2` (accepted M1 plus its documentation). S7-A01 is accepted: PR #10 merged final feature head `3195d09` into `master/a925140`, with complete exact-head push/PR and merged-master CI. The original local checkpoints and initial failed delivery runs remain recorded below; [final acceptance](#2026-10-18-s7-a01-functional-acceptance) distinguishes their scope. S5-03, M2 and the full S7-A stage remain incomplete.

## Scope And Ownership

- Protocol: independent native request/items/tools/usage/terminal events, explicit capability selection, public/upstream identity separation; never via Chat conversion.
- Gateway: the existing execution/admission/attempt/legacy finalization chain consumes Responses; strict/claimed Responses fail closed before monetary effects.
- Application: `internal/responses` owns persistent scoped idempotency and short-lived encrypted foreground replay, not scheduling or a second financial ledger. Store owns atomic identity/quota/CAS and schema migration v11.
- Composition/HTTP: opt-in finite configuration, mounted-key startup validation, exclusive recovery, bounded cleanup/join, authenticated route, CORS headers, OpenAPI 0.3.0 / 80 operations.
- Regression scope: real temporary SQLite, native loopback upstream, authenticated HTTP, disabled/legacy JSON/SSE, duplicate RPM/pre/settle avoidance, concurrency/revocation/restart/unknown and key/financial-migration boundaries. Existing Chat/embeddings, browser, smoke and delivery gates remain intact.

The exact behavior, retention, store:false refusal, stateful exclusions and future background atomic boundary are specified in [Responses HTTP contract](../reference/responses-http.md). No real upstream or production database has been used.

## Development And Verification

Implementation and risk regression authoring were batched before test execution, as requested. Concentrated local verification and repairs are complete. All commands below ran from the repository root on Windows/PowerShell, Go 1.26.3 (tool host reports windows/386; **every Go verification command explicitly set GOARCH=amd64**), Node 24.14.1 and Python 3.14.3. Tests use temporary SQLite and isolated fake/loopback providers only.

Final executable/configuration/contract source fingerprint: `4ddd92c464f17e5dae7994712c172435689cc93e5d52295109200f0c50417a86`, covering 285 non-Markdown tracked and non-ignored new files. This is SHA-256 of a sorted path-to-content-SHA-256 JSON map (compact separators); the fingerprint matched before and after final validation. It identifies this uncommitted worktree on base `bea97fa`, not a Git commit or remote acceptance. No executable file changed between that final local suite and initial feature submission `41382bc`. The later CI-driven delivery-test repair is separately scoped below and does not modify Go/configuration/API behavior.

| Command / scope | Actual result |
|---|---|
| `$env:GOARCH='amd64'; go test -json -count=1 ./...` | Final source: 1003 tests/subtests passed, 24 packages passed, no failures. Five subprocess-only worker helpers skip in their parent invocation; their enclosing crash tests execute normally |
| `go build ./...`; `go vet ./...` with the same target override | Both passed on final source |
| `go mod tidy`; `git diff --exit-code -- go.mod go.sum` | Passed; no dependency changes |
| `go list -deps ./internal/core ./internal/protocol/...` plus forbidden dependency scan | Passed; no Gin/GORM/business/store dependency escapes core/protocol |
| `node --check` for app/API modules and all three browser scripts; `node --test web/assets/api.test.mjs` | Syntax checks passed; 10 API-client tests passed |
| `node tests/browser/navigation.cjs`; `node tests/browser/billing.cjs` | Passed with preinstalled Playwright/Chromium paths supplied through NODE_PATH and PLAYWRIGHT_EXECUTABLE_PATH; desktop/mobile, session/role, ledger and saved/publication behaviors preserved |
| `python tests/smoke/compatibility.py --go go` | Passed: disposable production binary, real API/UI channel/credential publication, restricted models/key, playground, logs and role workflow; temporary artifacts cleaned |
| `python tests/smoke/local.py --go go` | Passed again on final source: existing production startup/auth/Chat/SSE/console, strict refusal, real main forced kill/restart/pending/waiver, encrypted credential/runtime key recovery and shutdown |
| Changed-Go normalized-line-ending gofmt comparison | 44 changed/new Go files passed; only modified Go files were formatted |
| `git diff --check`; changed-Markdown local links/anchors | Passed: 11 changed Markdown files and 160 local links/anchors; no Go retest for text-only evidence updates |

The browser/API/UI scripts and frontend sources were unchanged after their successful executions. The later security/long-SSE repairs affect native Responses or the archive cap; final full Go tests include native HTTP/SSE, old archive/worker, strict and Chat/embeddings regressions, and main smoke was rerun after those repairs. Browser results are reused for that unchanged consumer scope, not misrepresented as a Responses browser/client acceptance.

### Failures And Concentrated Repairs

1. First full Go run: 992 tests/subtests passed, five helpers skipped, one newly authored runtime test failed. Its fake upstream fixture omitted mandatory `object:"response"`; corrected the fixture without weakening native decoder validation. `go test -count=1 ./cmd/gateway` passed on repair. Later final full suite supersedes that intermediate result.
2. First browser invocation lacked NODE_PATH and could not load the already installed Playwright package. No app failure or dependency installation occurred. Repeating only the failed browser commands with the existing bundled module and Chromium paths passed.
3. Independent review found native non-200 HTTP/transport errors could bypass decoder sanitization. The Responses gateway boundary now replaces provider message/type/code before logs, health feedback, HTTP errors or encrypted output; phase/status/usage/commit facts remain. The regression verifies private URL/key strings never reach any consumer and no retry occurs.
4. Review found recursive response identity/error rewriting could mutate legal string metadata or opaque output extensions. Rewriting now touches only documented JSON/SSE envelope positions; dedicated native preservation tests passed.
5. Review found the old 256-segment archive bound could truncate an otherwise small 300-event SSE. A shared finite 8192-segment bound now governs append, result-slot reservation, read validation and bounded preload. Byte quotas, the final 4 KiB reserve, authorization, CAS and ciphertext format remain unchanged. Real authenticated Responses HTTP regression emits 320 deltas plus terminal, verifies exact sealed replay and one upstream execution; exact-cap/corrupt-row tests cover safe read/append limits.
6. Foreground archive/downstream I/O failures and panic remain uncertain and non-replayable. A regression injects archive commit failure to verify it cannot become empty HTTP 200 or cause re-execution. Replay write deadlines are delivery-scoped and cleared, and replay retention must cover configured generation timeout plus finalization.

Focused repair commands used `GOARCH=amd64`: `go test ./internal/gateway -run '^TestResponsesNonNativeErrorsAreSanitizedBeforeOutputLogsAndHealth$' -count=1`; `go test ./internal/protocol/responses ./internal/protocol`; `go test -count=1 ./internal/responses ./internal/config`; and `go test ./internal/generation ./internal/store ./internal/server -run 'TestGenerationArchiveSegmentCapReservesFinalSlotAndBoundsCorruptReads|TestGenerationArchiveRefusesOversizedCorruptBlobBeforeLoading|TestResponsesHTTPNativeJSONSSEAndDurableIdempotency|TestForeground' -count=1`. All passed, followed by the complete final suite above.

### Outstanding At The Local-Delivery Checkpoint

At the local-delivery checkpoint preceding feature submission, no batch commit/push/PR/merge had been made, and matching remote integration was pending. Linux full race, container installation/upgrade/backup restoration, automatic push/PR/master CI and latest-base integration were still required before checking S7-A01 acceptance. These requirements have now passed on the exact final source as recorded below; existing workflows and gates are unchanged. Historical M1 green runs are not evidence for this new source. No real provider/account, vendor cost bound, SDK/Codex client, S7-A02 history/continuation, background or native WebSocket acceptance is claimed. S5-03 and the full S7-A stage remain incomplete.


## 2026-10-18 Initial PR And Delivery Regression Repair

Initial feature head `41382bcdafcfde78013fb3ef3dbd26ff6afed8c3` was pushed once from the unchanged latest `master/bea97fa` baseline and submitted as [PR #10](https://github.com/ANRCM0/TXAPI/pull/10). Its automatic push [39072651853](https://github.com/ANRCM0/TXAPI/actions/runs/39072651853) and PR [39072715099](https://github.com/ANRCM0/TXAPI/actions/runs/39072715099) both finished with **check SUCCESS / delivery FAILURE**, so neither complete run was accepted. The check job includes full Linux race, dependency/contract/build/vet/format, frontend and main smoke; its success is exact to `41382bc`, not the later repair commit.

The delivery log identifies `tests/smoke/delivery.py`'s stale expected migration tail `[5,6,7,8,9,10]`; the application correctly added schema v11, causing that exact-version assertion to fail. The concentrated repair updates the expected tail to include v11, requires an empty `response_requests` table for fresh/upgrade disabled deployments, and includes that optional current-only table in snapshots so exact S3 backup restoration also proves it does not remain after rollback. Original released migration rows/timestamps, price-history digests, financial log associations and in-flight SSE drain assertions are retained. The PASS label now reports current/v11 rather than stale v9.

Only this executable test consumer and this evidence document change in the repair; Go production/tests, configuration, OpenAPI, dependencies and workflows are unchanged. AST parsing/source compilation, `python tests/smoke/delivery.py --help`, and an isolated temporary-SQLite snapshot/restore-shape check passed. The ad-hoc Windows checker initially failed temporary-file cleanup because its own sqlite context did not close the connection; explicit closing fixed that checker without changing production or delivery logic. The repaired non-Markdown source fingerprint is `e3b23f49d526a47564d2070b0e6bd2d8490962adb298e60e7a70f73b122b8eab` (same 285 paths). Existing local Go/browser/main evidence is reused only for unchanged consumers; the corrected container path still requires a new complete automatic push/PR Verify run on the repair head. No manual workflow dispatch or CI bypass is used.

## 2026-10-18 S7-A01 Functional Acceptance

The repair was committed as `3195d0923d957eb5f41dde5412912d10e72259f7` after initial feature head `41382bcdafcfde78013fb3ef3dbd26ff6afed8c3`. Fresh fetch before merge confirmed the latest master still matched source base `bea97faefce65fc5ecf74b8703690c71b2f8cfe2`. The exact final head had four successful check/delivery results across automatic push and PR workflows, and PR #10 was mergeable/clean. `gh pr merge 10 --merge --match-head-commit 3195d0923d957eb5f41dde5412912d10e72259f7` merged without administrator override or bypass.

Actual merge: `a925140ab86fca780a71ef6956a45e0e6af38658`, parents `bea97fa` and `3195d09`, at **2026-10-18 06:04:01 Asia/Shanghai** (`2026-10-17T22:04:01Z`). A fresh fetch confirmed that master; its tree is identical to the final feature head. The actual merged-master automatic Verify run then completed successfully, independently of the feature-head runs.

| Exact source / event | Complete Verify result |
|---|---|
| `3195d0923d957eb5f41dde5412912d10e72259f7`, push | [39073160376](https://github.com/ANRCM0/TXAPI/actions/runs/39073160376): check SUCCESS, delivery SUCCESS |
| Same final feature head, PR #10 | [39073164111](https://github.com/ANRCM0/TXAPI/actions/runs/39073164111): check SUCCESS, delivery SUCCESS |
| `a925140ab86fca780a71ef6956a45e0e6af38658`, merged master | [39073532710](https://github.com/ANRCM0/TXAPI/actions/runs/39073532710): check SUCCESS, delivery SUCCESS |

These are the existing unmodified Linux/ubuntu-latest Verify jobs with the pinned Go/actions configuration: tidy/dependency and architecture/API contracts, build, native frontend behavior, vet, gofmt, full race, coverage reporting, production-main smoke and performance reporting; delivery covers container fresh installation, v11 upgrade, stopped backup/old-version restore and in-flight SSE drain. Results were read using `gh run view ... --json headSha,status,conclusion,jobs` and `gh pr view 10`; `gh run watch ... --exit-status --interval 60` only followed existing automatic runs. No manual dispatch, disabled regression or weakened gate was used. The earlier failed complete runs remain failures, not retroactively green.

### A01 Requirement Proof

| PLAN A01 requirement | Delivered behavior and verification |
|---|---|
| Independent native operation/items/events | `protocol/responses` preserves text/function tools/history/instructions/output limits/usage and native terminal semantics; protocol/decoder and real JSON/SSE HTTP regressions pass, without Chat conversion |
| Shared typed generation and financial lifecycle | Existing `Gateway.ExecuteTo`, admission, attempts and legacy finalization are used; disabled/legacy native JSON/SSE passes. Strict and claimed Responses reject before admission/holds/attempt. Future background atomic admission remains the real existing intent port, not a second queue/ledger |
| Identity and scoped idempotency | Random generation/public response IDs, private upstream ID, canonical request HMAC and SQLite owner/Key unique/CAS authority; same-key conflict/concurrency, reauthorization, restart unknowns and one-execution/one-charge replay regressions pass |
| HTTP and existing consumer regression; strict safety | Authenticated native JSON/SSE including 320 deltas, failures, I/O/keys/TTL/capacity pass together with existing Chat/embeddings, frontend, strict, migration, main and delivery checks. There is currently NO cost-proven strict Responses subset: all strict Responses remain explicitly refused, not silently downgraded |

Only the four A01 checklist items are closed by this evidence. A01 retains default-off configuration, real credential encryption plus mounted content/digest keys, private finite replay and fail-closed unknowns. It does not provide GET/delete/input-item or conversation APIs, parent continuation, persistent public history, native background/WS, SDK/Codex or real-provider certification. A02-A06, M2 and full S7-A remain open; S5-03 remains independent and incomplete.

### Acceptance Documentation Scope

The follow-up `docs/s7-a01-acceptance` branch starts from accepted `master/a925140`. It synchronizes status, the four A01 checks and the separate contract/evidence boundaries; it changes Markdown only, not executable source/configuration/OpenAPI/dependencies/workflows. Local `git diff --check`, 12 changed documents / 176 local links and anchors, and independent rule/status review passed on Windows/PowerShell. The 285-file non-Markdown fingerprint remains `e3b23f49d526a47564d2070b0e6bd2d8490962adb298e60e7a70f73b122b8eab`. No redundant Go/browser run was made for these text-only edits. Existing automatic push/PR/merge CI still applies. The documentation's own commits and final CI/merge results are recorded in PR comments after they occur, avoiding an infinite evidence-only commit cycle.
