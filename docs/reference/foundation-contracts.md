# A3/A4 foundation contract audit

Status: A1-A4 foundation accepted on 2026-10-18. PR #6 merged final feature
head `56a205c` into `master/ccbfe29`; both exact-head push/PR CI and merged
master complete CI passed. Local final build/vet, 859 tests/subtests (five
subprocess-only helpers skipped), frontend and production-main smoke passed.
See the final history record for commands, failed-first-run repairs, exact
source/baseline and CI links. This is not complete S5/S6/S7 product acceptance. The authoritative
checklist remains [PLAN](../PLAN.md), with prior exact-SHA evidence in
[implementation history](../history/implementation-s5-a3.md).

## Scope and ownership

| Boundary | Owner and contract | Extension rule |
|---|---|---|
| `core.Output` / `core.Executor` | Core team; one synchronous encoded execution, monotonic commitment, standard library only | No user/store/billing/WS/session logic |
| `protocol.Request` / adaptor capability | Protocol team; operation/body ownership and explicit fidelity/rejection | No network, auth, ledger or task workers |
| `Gateway.ExecuteTo` | Integration; one fresh generation scope using trusted current Principal | Shared HTTP/worker/WS application path; no cloned coordinator |
| `Gateway.ExecuteClaimedTo` | Integration; same path plus fenced original reservation adoption | Private durable consumer; not a caller-supplied budget bypass |
| `LegacyBilling` / `StrictBilling` / `IntentBilling` | Billing implementation, gateway consumer ports | Original financial authority, no second ledger; new strict dimensions require independent acceptance |
| `IntentAdmission` / `IntentLeases` | Generation consumer ports, original Engine transaction implementation | Atomic hold/admission, hashed capability/epoch, no transaction callbacks into application |
| `ArchiveStore` / `Archive` / `CursorReader` | Store owns bounded CAS persistence; generation owns content AEAD, current authority and authenticated sealed replay snapshots | Resumable encoded-byte cursor, not a public product event ID or live event API |
| `Store.ResolvePrincipal` | Identity/store; current owner/Key/model and limit snapshot per generation | Binding IDs are not credentials; queued permissions are never authority |
| `transport/websocket` / `ConnectionCapacity` | Transport I/O / access capacity respectively | Physical connection lifetime is distinct from generation and billing |
| `secrets` | Shared standard-library-only primitive package | No DB/auth/lifecycle; callers supply domain-separated identity metadata |
| main/config/model/migrations/OpenAPI/CI | Integration-owned shared files | Coordinate one version/registration point, preserve existing CI and contracts |

These are real consumers, not declarations of stable S7 product behavior. The
A3-05 ports are accepted internal extension contracts for the existing operation
scope. Changes still require coordinated consumer review; they are not a public
SDK or native S7 wire/event contract. Public API contract remains
OpenAPI 0.2.3, 79 operations; no new Responses/session/task/WS route is added.

## Lifetime and failure invariants

- Physical connection, observation, queued intent, generation and network
  attempt are distinct. Observation cancellation cannot silently cancel durable
  work. Explicit cancel/deadline/shutdown stops dispatch and joins the existing
  execution/finalization chain; it is not proof of zero upstream cost.
- Admission atomically commits encrypted input, scoped idempotency and the
  original user/Key dual hold. Acknowledgment loss does not allocate another
  hold. Dispatch adopts that reservation and historical selling-price snapshot;
  changed provider assumptions reject rather than replacing the admission basis.
- Claim capability is secret and never stored raw. A new epoch is possible only
  while no durable attempt/provider occupation exists. Every execution ledger
  write is fenced; expired ownership cannot grant another attempt. Once a durable
  start commits, even a crash before the physical send is conservatively unknown.
- No database transaction spans network I/O. Fixed polling workers, lease and
  admitted deadline, process admission, encrypted input/output record/byte limits
  and retention are finite. There is no second volatile dispatch queue.
- Success publication requires both settled financial state and sealed committed
  output. A durable usage receipt may recover historical settlement without
  fabricating output success. Unknown attempted execution keeps holds/provider
  occupation and is never blindly replayed or refunded.
- The application root owns startup/stop/join. Stop producers before shared
  limiter/gateway/health/log/store dependencies. A timed-out join does not grant
  permission to close dependencies while callbacks are still using them.
- Single instance, exclusive startup, SQLite, single binary and minimal core
  remain the supported deployment. At-rest security acceptance requires the
  [encrypted credential/content configuration](secret-storage.md); unencrypted
  HTTP compatibility storage is explicitly not that security baseline.

## Failure matrix and evidence routing

The new matrix ran during concentrated local integration, including the real
five-window process-death tests. The first repaired full Go run passed 851 tests/subtests
and skipped four subprocess-only helpers; the later added encrypted seed/pool
replacement test separately passed on unchanged production source. Six frontend
behavior tests and production-main smoke also passed. Windows process termination
is not evidence of graceful SIGTERM. Final complete Linux CI separately passed
signal handling, full race and container delivery on the accepted source; the
local Windows result is not used as a substitute.

The first remote runs on `08f67a6` passed container delivery but failed Linux
race: lease renewal captured the execution context variable while dispatch
replaced it with an authorized wrapper. The fix passes the initial deadline
context as an immutable goroutine argument. Local full checks were repeated;
the failed runs are not acceptance evidence. The fix and the later cursor
addition passed replacement exact-head complete CI before merge.

The final audit also identified that an append sequence alone was not evidence
of resumable observation. The added `Archive.OpenCursor` / `CursorReader.Next`
consumer provides an authenticated encoded-byte position and a bounded sealed
snapshot. Its concentrated local build/vet/full-test/smoke checks passed:
859 tests/subtests passed and five subprocess-only helpers skipped. Frontend
sources and dependencies are unchanged, so their earlier checks remain applicable.
The earlier green `d6550d3` CI did not validate this addition. The final
`56a205c` push/PR runs and merged `master/ccbfe29` complete CI now cover it.

| Failure or boundary | Required observation | Regression location |
|---|---|---|
| Admission/claim/cancel SQL failure | Intent and original dual hold transition atomically; no partial authority | `billing/intents_test.go`, `billing/intent_execution_test.go` |
| Concurrent claim, expiry and new epoch | One owner; all stale financial writes rejected; no resurrection | `billing/intent_execution_test.go` |
| Price/provider policy drift | Original selling history; different provider assumptions refuse | Billing claim and generation durable tests |
| Current Key/owner/model revocation | No queued request sent under old permission; same-owner other Key denied | Generation runner/archive/intent/durable tests |
| Content tamper, missing key, bad key file | Generic fail-closed error; no partial body/free hold/replay | `secrets/secrets_test.go`, generation archive/intent/key-file tests |
| Credential conversion/rotation/write failure | No plaintext SQL in encrypted mode; atomic activation; old keys required | `store/credential_keys_test.go` |
| Logical capacity/output failure | Reject before send or fail along original execution chain; release process slots | Store archive, generation archive/durable tests |
| Heartbeat/cancel/shutdown | Renewal is bounded; attempted cancellation preserves unknown cost; join I/O/finalization | `generation/durable_test.go` |
| Death after claim | Proved-unstarted work may be claimed under a new epoch before deadline | `generation/durable_crash_test.go` subprocess |
| Death after durable attempt | No resend; pending dual holds/provider occupation | Same subprocess matrix, `after-start` |
| Death after receipt/financial finish | Historical idempotent settlement; incomplete output is not success | Same subprocess matrix, `after-receipt`/`after-finish` |
| Death after sealed output | Recover final success, never a second attempt/hold | Same subprocess matrix, `after-seal` |
| Recovery batch fairness / TTL | Reclaimable rows do not starve attempted recovery; expired body purged without deleting unknown audit | Billing claim/intent tests |
| Output cursor / observer death | Authenticated bounded snapshot; current authority, exact byte resume after a real process kill/key rotation, no new execution/hold; cancellation/Close releases observation capacity | `generation/cursor_test.go`, `generation/cursor_crash_test.go` |
| v9 upgrade/restart/failing v10 indexes | Preserve old intent/ledger/credential identity; no invented permission; old binary refuses | `store/migrations_test.go` |
| Actual main startup/restart/invalid keys/signal | Encryption activation before producers; missing/wrong secrets reject; Linux SIGTERM joins | `cmd/gateway/generation_test.go`, `tests/smoke/local.py` |
| Existing connection lifecycle/protocol/HTTP | Existing JSON/SSE/WS I/O/auth/limit behavior remains intact | Existing complete regressions and CI |

A4 audit against PLAN/ARCHITECTURE/COLLABORATION is closed for the foundation
scope above. Exact feature head and latest integration baseline, complete CI
(including Linux race, container v10 upgrade/restore and signal handling),
actual merge and environment are recorded in the implementation history.
Master run [#39055782687](https://github.com/ANRCM0/TXAPI/actions/runs/39055782687) passed on `ccbfe29545dec358da9d9cda4b7ac3624ded1ec0`. No historical green run
substitutes for the final source.

## Product and governance exclusions

Native Responses production routing, durable conversations, provider-native
background execution, public/native event ID contracts, native upstream WS/multiturn clients,
and S7 acceptance remain separate functional batches. An internal output segment
sequence alone does not prove a resumable consumer; the private encoded-byte
snapshot cursor is distinct from a public/live protocol event contract. This does not remove
the required private persistence/fencing/recovery/security acceptance for A3/A4.

Directory ownership is currently a documented role, not evidence that GitHub
enforces CODEOWNERS or required reviews. Required checks/review settings must be
verified with sufficient repository permissions before claiming enforced team
governance. Do not invent GitHub teams, bypass review/checks, or label an unreadable
protection API as enabled. The technical common-baseline merge and platform
protection verification are separate recorded facts.
