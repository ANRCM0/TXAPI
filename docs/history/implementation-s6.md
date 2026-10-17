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
`758ad04` integration baseline. Exact-source CI and the actual feature merge
are recorded separately below; local passes do not certify later source.

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
downloaded. Linux full race and existing container delivery were subsequently
executed by exact-source CI, not inferred from local Windows passes.

## 2026-10-18 S6 framework acceptance and M1 baseline

| Fact | Exact source and scope |
|---|---|
| Latest integration baseline | `758ad04fb82dcf70dd4cadec039d15cd2a8629a7`, fetched at start and again immediately before merge; it remained the feature ancestor and PR base |
| Protocol slice | `060a2eb`, content/tool stream fidelity and final usage |
| Management slice | `c349bc4`, shared configuration persistence/publication use cases |
| Final feature head | `9a6eaa250099c91847fa92ffde0475af16b6e488`, frontend compatibility, real consumers and the complete S6 batch; clean tree after commit/push |
| Exact-head push CI | [Verify 39062579429](https://github.com/ANRCM0/TXAPI/actions/runs/39062579429), SUCCESS for both `check` and `delivery` |
| Exact-head PR CI | [Verify 39063298294](https://github.com/ANRCM0/TXAPI/actions/runs/39063298294), SUCCESS for both `check` and `delivery`; all four push/PR checks were confirmed green with `MERGEABLE` / `CLEAN` before merge |
| Actual feature merge | [PR #8](https://github.com/ANRCM0/TXAPI/pull/8), merged at 2026-10-18 03:43:33 Asia/Shanghai (2026-10-17 19:43:33 UTC); merge `ca009c2a1e3aa00bfb978f9d9e48785db979763a`, parents are the latest baseline and exact verified feature head |
| Merged-master CI | [Verify 39063693685](https://github.com/ANRCM0/TXAPI/actions/runs/39063693685), SUCCESS for both `check` and `delivery` on exact `ca009c2a1e3aa00bfb978f9d9e48785db979763a` |

Merge used an exact-head match, without admin/bypass. No workflow trigger,
existing regression or gate was disabled, and no workflow was manually
dispatched. Complete CI includes module cleanliness, architecture/dependency
and all 79 OpenAPI operation checks, build/vet/gofmt, frontend syntax/behavior,
full Linux race, the existing production-main smoke, and container installation,
S3 upgrade, backup/restore and shutdown. Coverage and performance are reports,
not claims of an improvement or additional external compatibility.

The PR audit mapped S6-00 through S6-05 to actual code, consumers and the
recorded local/CI evidence, and found no remaining promised-scope functional
gap. The new real API/browser workflow ran locally with the temporary production
binary; its execution is not attributed to the CI syntax-only browser check.
Unchanged A1-A4 behavior reuses its recorded evidence only where applicable;
the final S6 full CI also executes all existing Go/race and smoke regressions.

The completed S6-00 through S6-06 audit is scoped as follows:

| Requirement | Delivered behavior and authoritative proof |
|---|---|
| S6-00 inventory | All 79 operations, application consumers, support matrix and G01-G11 findings are mapped in the compatibility inventory; unchanged, repaired and unsupported behavior are distinguished |
| S6-01 core/business | Shared gateway coordination and HTTP admission handoff remain; typed-nil injection and missing output fail before dispatch. New injection/output cases and existing authorization, worker/socket, lease, budget and finalization regressions pass in final full CI |
| S6-02 protocol/tools | OpenAI structured content, Anthropic initial blocks/multi-tool parameters and bounded stream sequencing, Gemini cumulative/tail usage and usage-only chunks are preserved; all protocol packages pass after the fixture-only repair, then final full Linux race passes |
| S6-03 management/API | Both legacy and session HTTP consumers use real `management.Configuration`; real Store tests cover save/readback errors, publication ordering and Apply without replay. Existing DTO, route/authentication and owner/resource behavior pass final contract and full tests |
| S6-04 extensions | Existing injected adaptor dispatch and copied-map isolation are actual consumers; invalid injection, per-stream sessions, capability rejection, dependency and lifecycle rules are recorded and regression-checked, without a dynamic loader or empty interface platform |
| S6-05 frontend | Ten API behavior cases plus navigation/billing regressions cover saved/applied errors, role/session changes, stale responses, exact money and pagination. Temporary production binary/SQLite and real API/browser flow pass at desktop/mobile sizes; existing main smoke also passes |
| S6-06 integration | Exact feature head and latest ancestor/base confirmed, full push/PR CI green, PR #8 actually merged with matching head and merged-master full CI green; PLAN and the compatibility/application/extension guides synchronize these observed facts |

S6-A/B/C and M1 are accepted for the declared existing support range on
`master/ca009c2`. No application support promise was replaced with a refusal
to obtain acceptance. S5-03/full cost proof, new strict permission, S7-A
production Responses/conversations/native background and product WS, arbitrary
external clients, real providers and production database operation remain
outside this S6 acceptance. This documentation-only update records already
observed source results; those runs do not certify the subsequent documentation
text itself. It adds no executable source, migration, dependency or contract
change and does not require another local functional test run/manual CI dispatch.

## Acceptance documentation synchronization

The follow-up `docs/s6-m1-acceptance` starts from the verified functional merge
`ca009c2`. It changes only eight Markdown files: the active PLAN, README,
architecture/development/collaboration guides and S6/foundation references and
history. Windows/PowerShell `python .local/validation/check-foundation-docs.py`
checked 131 local links/fragments across those eight changed documents;
`git diff --check` and the scoped diff/status review passed. The checker is an
ignored local validation helper, not a new product dependency. No additional Go,
browser or smoke tests were run for this pure explanation/status update; normal
automatic push/PR CI triggers remain unchanged. Final documentation integration
and its exact CI results can be recorded on the PR, rather than creating
recursive evidence-only source commits.
