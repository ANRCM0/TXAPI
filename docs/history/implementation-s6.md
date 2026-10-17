# S6 implementation and acceptance record

## 2026-10-18 implementation batch

Objective: execute S6-00 through S6-06 from the active PLAN. The user explicitly
requested no tests during implementation, followed by concentrated late testing
and repairs. The final complete CI and integration gate remain required.

Source baseline: fetched `origin/master/758ad04`; branch
`feat/s6-compatibility-framework`. Existing A1-A4 foundation evidence remains
scoped to its original source and does not certify these S6 changes.

Implemented scope:

- S6-00: [all 79 operations and actual consumers](../reference/compatibility-contracts.md),
  support matrix, injection lifetimes, application contracts and concrete gaps.
- S6-01: reject typed-nil adaptor/admission/limiter injection and missing output
  before dispatch; retain HTTP middleware's single rate-admission handoff.
- S6-02: preserve string/array content shape, Gemini cumulative and usage-only
  tail observations, usage-only choices arrays, and Anthropic initial block/tool
  arguments with bounded per-stream state and explicit invalid-sequence errors.
- S6-03: actual `management.Configuration` save/publication use case, shared by
  administrator-session and legacy HTTP handlers; existing Store transactions,
  response DTOs, publication status and saved-error behavior remain authoritative.
- S6-04: constructor registration and session/lifetime rules use real existing
  extension consumers, with new invalid-injection behavior and risk regressions.
- S6-05: structured saved/applied errors, retry state, list sequencing/pagination
  recovery, current-role refresh and old-session response rejection. A real
  temporary API/production-binary/browser workflow joins the existing UI.

The original concurrent publication regression moved with the state machine to
management. Existing stream fixture regressions retain their purpose, with
valid content block start/stop events. Other existing tests remain in place.

No schema migration, Go dependency, new public route or contract version is
introduced. The OpenAPI contract remains 0.2.3 with 79 operations. S5 cost proof
and S7 product delivery retain their own acceptance gates.

Development actions were source reads/reviews, edits, gofmt on changed Go files
and diff review only. All new behavior and browser regressions were authored
without execution; testing began after implementation and cross-review.

## Concentrated local validation and repair

Environment: Windows/PowerShell, Go 1.26.3 (host windows/386; every Go command
explicitly set `GOARCH=amd64`), Node 24.14.1, Python 3.14.3, installed Playwright
with Chromium 1208. No real upstream, production database or external client
credential was used. The candidate is the S6 working source on the fetched
`758ad04` integration baseline; final exact-source CI and merge are pending.

| Actual command / check | Result and scope |
|---|---|
| `go test -json -count=1 ./...` | First run: 926 tests/subtests passed, five subprocess-only helpers skipped; one new OpenAI content-array fixture failed strict JSON validation |
| `go test ./internal/protocol/adaptor/openai -run '^TestContentArraysAndNullRemainStructured$' -count=1` | Passed after removing a single extra closing brace from the fixture; production codec/strict validator unchanged |
| `go test ./internal/protocol/... -count=1` | All affected protocol packages passed after that repair; unchanged non-protocol packages reuse their passing first-run results |
| `go build ./...`, `go vet ./...` | Passed in the approved environment; the sandbox first failed to read shared Git metadata for VCS stamping, which was not disabled |
| `go mod tidy`, `git diff --exit-code -- go.mod go.sum` | Passed; no dependency/Go baseline change |
| `go list -deps ./internal/core ./internal/protocol/...` plus forbidden-import check | Passed; HTTP framework, ORM and business/store dependencies absent |
| `node --check` on app/api and three browser scripts; `node --test web/assets/api.test.mjs` | Syntax passed, ten behavior cases passed |
| `node tests/browser/navigation.cjs` | Passed: cancellation, stale responses, log ordering/page retry, current-role/403, saved-applied/pending errors and publication retries |
| `node tests/browser/billing.cjs` | Passed: exact units, historical snapshots, audited decisions, failed-page recovery, role navigation and desktop/mobile layout |
| `python tests/smoke/compatibility.py --go go --node node` | Passed: temporary production binary/SQLite, real API and browser channel/credential save, publication, restricted user/Key, playground, logs and 1440/390 viewports; cleanup completed |
| `python tests/smoke/local.py --go go` | Passed: production startup/auth/JSON/SSE/console, strict refusal and settlement, forced-kill/restart/reconciliation, credential and encrypted-runtime key/recovery/closing checks |
| Normalized-EOL whole-repository gofmt comparison | All 243 Go files matched; only changed Go files were formatted, existing CRLF files were not rewritten |
| `git diff --check`, Markdown local links/anchors and source review | Passed; independent review found no reproducible new high-risk defect |

The real browser fixture initially assumed a page reload preserved an in-memory
session; source review corrected it to consume the actual Settings navigation.
Its first executing run then exposed an ambiguous Models locator and an
unhandled response-wait rejection. Stable navigation selectors and a joined
response/action wait fixed the fixture. Error reporting now includes a safe
phase and redacted first-line diagnostic. The final real browser run passed.
The sandbox build failure was environmental; the identical build/fixture command
passed with approved shared-Git access. It was not bypassed with build flags.

Browser commands used the existing bundled module path via `NODE_PATH` and
`PLAYWRIGHT_EXECUTABLE_PATH` pointing to installed Chromium. No dependency was
downloaded. Linux full race and existing container delivery remain the final CI
gate and are not inferred from local Windows passes. S6-A/B/C remain unchecked
until complete CI and integration acceptance exist.
