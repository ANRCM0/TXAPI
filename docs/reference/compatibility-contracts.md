# S6 compatibility and consumer inventory

Source baseline: `758ad04` (fetched `origin/master` on 2026-10-18).
Scope: S6-00 through S6-06 from [the active plan](../PLAN.md).
The accepted S6 OpenAPI baseline is 0.2.3, with 79 operations. This inventory maps
source and declared contracts; it does not replace behavior acceptance.
S6-A/B/C and M1 are accepted on `master/ca009c2` (PR #8). Exact final feature
head `9a6eaa250099c91847fa92ffde0475af16b6e488` passed complete push/PR CI,
and the actual merged master passed complete CI separately. The user requested
no tests during implementation; concentrated tests and repairs began only
after the batch was implemented.
Commands, first-run failures, repairs and environment are in
[S6 implementation history](../history/implementation-s6.md).

S7-A01 separately extends the accepted contract to 0.3.0 / 80 operations.
PR #10 merged final feature head `3195d09` into `master/a925140`; its own complete
push/PR/master CI passed. Its opt-in foreground Responses route and receipt/replay
consumer are specified in [Responses HTTP](responses-http.md), with
[exact A01 evidence](../history/implementation-s7-a01.md#2026-10-18-s7-a01-functional-acceptance).
The historical S6 inventory and evidence below do not validate that extension.

A02 accepted through PR #12 / `master/765501a` extends OpenAPI to 0.4.0 / 91 operations
with 11 history/conversation routes and full local-context continuation. See
[history contract](responses-history.md) and [A02 evidence](../history/implementation-s7-a02.md).
It has its own encrypted snapshot/CAS/target consumers; no historical S6/A01 CI
is substituted for A02 verification. Exact head `2084bcd` and merged master
passed their own complete push/PR/master CI; M2 is accepted.

FC-001 FND-01/02/03 have separately passed their exact-head and merged-master gates. FND-04 has migrated the remaining application/HTTP consumers; its local evidence and pending final CI are recorded in [FC-001 history](../history/implementation-fc001.md). Public candidates remain unfrozen until FND-05.

## Responsibilities and actual consumers

| Responsibility | Implementation / consumer | Ownership rule |
|---|---|---|
| One upstream HTTP attempt and encoded output | `core.Executor`, `core.Output`, `core.HTTPOutput`; gateway consumes them | Standard library only; execution/commit facts, cancellation and bounded I/O |
| Generation business lifecycle | gateway shared `execute` / `ExecuteTo` / `ExecuteClaimedTo` | Trusted current principal, one admission, routing/attempts, original ledger and one finalization |
| HTTP model calls | server authentication/rate ports, compat/generationhttp | Preserve the middleware admission handoff; non-HTTP generation resets inherited mutable marks |
| Non-HTTP generation | generation Runner / durable dispatcher; existing socket application regression | Refresh identity per dispatch; default-off runtime and existing capacity, ownership and encryption constraints |
| Upstream representation | protocol operation envelope and registered adaptors | Separate Chat/embeddings payloads; capability filter and send preparation consume the same requirements |
| Configuration use cases | `management.Configuration`; both legacy and administrator HTTP resource handlers | Serialize channel/credential/price persistence and publication; Store owns transactions, callbacks own snapshot application |
| Other management/owner use cases | controlplane via control/v1, original Store bridge; ledger/v1 for existing Engine | Current session/admin/owner boundaries remain; L2 consumes public capabilities, never implementation handles |
| Frontend application | `web/assets/api.mjs` and `app.mjs` | Session and call Key separated; stable DTO/error/pagination, current roles, exact ledger money, saved/applied visibility |
| Construction extensions | `gateway.Options.Protocols/Operations` plus optional billing/admission ports | Constructor registration is copied; instances must support concurrent reads, own per-attempt mutable state and honor cancellation |
| Assembly/persistence | cmd root; existing L1 config/model/store/secrets implementations | One main lifecycle, schema runner, identity authority and ledger; no new S6 schema or public S7 route |

## Contract rules

- `/v1/*` consumes CallBearer; administrator and owner APIs consume SessionBearer.
  The deprecated 24 legacy operations retain AdminKey/LegacyAdminBearer. Sessions
  are not model-call Keys, and ordinary Keys do not authorize management.
- Input DTOs and complete-update semantics remain those in OpenAPI. User/Key
  policies are intersections. An empty allowlist grants nothing. Owner resources
  remain scoped to the authenticated user; frontend navigation is not authority.
- Preserve each existing success status, envelope, nullable list and pagination
  shape. Request logs use offset/limit; ledger IDs and monetary units remain exact
  strings. Do not normalize all legacy errors into a new incompatible shape.
- A configuration write can be saved with publication pending, or return a
  `saved: true` 500 after commit/readback failure. Publication headers apply to
  that desired revision. Retry Apply, not the save; Apply retries do not create
  new desired revisions. Compare revisions only within their process epoch.
- A 503 from configuration Apply carries the latest ConfigStatus in `data`.
  The client must display it, retain the session, and allow a later Apply.
- Stream commitment, protocol completion, generation termination and financial
  settlement are different facts. Never append successful DONE after a read,
  decode or output failure. Cancellation is not proof of zero upstream cost.
- A constructor extension cannot open a new operation/transport merely by
  declaring it. Its body, capabilities and current policy still pass the same
  preparation, admission, attempt and finalization chain.

## Declared inference support

| Inbound operation / target | disabled / legacy | strict |
|---|---|---|
| Chat JSON/SSE / OpenAI | Native fields; function tools and history on same-protocol path | Registered official text/input/tariff/credential profile only; explicit bounded output; tools rejected |
| Chat JSON/SSE / Anthropic | Representable text/function tools, ID/result round trip and per-stream increments; unsupported options reject before send | No registered input profile; execution rejected |
| Chat JSON/SSE / Gemini | Text subset, cumulative usage; tool definitions/history reject | No registered input profile; execution rejected |
| Non-stream embeddings / OpenAI-compatible | Independent native body/vector decoder, including supported extension opt-in | Registered official input/tariff/credential profile only |
| Foreground Responses JSON/SSE / native OpenAI-compatible | Separately accepted A01 opt-in route; text/function tools, scoped durable idempotency and short encrypted replay | Rejected before admission/holds/attempt; no proven Responses subset |
| Responses local history/conversations | Separately accepted A02 via PR #12; full-context text/function subset and scoped encrypted snapshots | All strict Responses refused |
| Responses background/native WS product | Not implemented; independent foundation consumers do not provide these products | No inherited Chat or A01 approval |

The matrix states implementation scope, not independent provider billing or
external client certification. S5-03 remains open. Existing `RequirementsFor`,
`Supports`, candidate filtering and `PrepareRequest` enforce these constraints;
new tests do not expand strict permission.

## Gaps and disposition

| ID / batch | Source finding and affected consumer | Implementation disposition | Regression / proof |
|---|---|---|---|
| G01 / S6-01 | Typed-nil optional dependencies/adaptors can pass constructor checks; typed-nil Output can panic before execution | Constructor rejects explicitly invalid injection; ExecuteTo/claimed returns existing missing-output error | `gateway/s6_injection_test.go`; HTTP admission, output/cancellation and original A1-A4 regressions |
| G02 / S6-02 | A text content string containing `[]`, `null` or JSON-looking text can be reinterpreted as structured parts | Preserve the original string-versus-array shape through canonical conversion | OpenAI `wire_test.go`, request/tool/response/stream and real content arrays |
| G03 / S6-02 | Gemini usage-only tail events are ignored and later cumulative counts are discarded | Consume every observed usage snapshot, retaining the final one, without changing protocol termination | Gemini `stream_test.go`, protocol `gemini_usage_test.go`, read/EOF/finish regressions |
| G04 / S6-02 | Usage-only translated Chat chunks serialize choices as null | Encode an empty choices array for usage-only output | OpenAI builder and real Gemini-to-core SSE JSON regression |
| G05 / S6-03 | Save/publication ordering is owned by HTTP middleware, preventing reuse of the same management use case outside handlers | Real `management.Configuration` consumes Store and publication callbacks; both HTTP groups call the same service | Moved original concurrent-publication regression plus management real-SQLite save/error/retry/serialization cases |
| G06 / S6-05 | Client drops saved=true on a 500; editing can resubmit a committed create/write | Preserve structured error, close/clear completed form and refresh saved resources/publication state | Client and navigation saved-failure regression; no duplicate mutation |
| G07 / S6-05 | Apply 503 is not displayed and repeated Apply clicks race | Display returned status and guard the in-flight retry | Client/navigation Apply 503 -> retry -> applied flow |
| G08 / S6-05 | List refresh responses race; failed page loads advance offset/disable controls | Request sequence guards; commit offset only on success and recover original controls after failure | Navigation/log and billing pagination failure/ordering cases |
| G09 / S6-05 | UI retains login-time role until a new login | Read existing /api/me on navigation; recover from 403 with bounded identity refresh, without logout | Navigation role-change and client stale-session response cases |
| G10 / S6-04/05 | Existing browser scripts use fake API responses, with no real frontend/API acceptance chain | Add real-server API integration and a disposable live browser fixture | `server/s6_integration_test.go`, `tests/browser/live.cjs` and temporary production-main fixture |
| G11 / S6-02 | Anthropic start content is discarded and an argument-free tool can end without a JSON object | Preserve initial text/object input and emit an empty object at tool stop only if no arguments were observed; reject inconsistent block sequences | `anthropic/stream_blocks_test.go`, protocol `anthropic_blocks_test.go`, existing tool/stream isolation cases with valid start/stop |

This list preserves applicable implemented A1-A4 behavior. Source review found
no need to recreate the HTTP executor, shared generation chain, worker identity
resolution, fencing, output cursor or financial authority. Exact source changes
passed their own concentrated verification and exact-source complete CI;
old CI alone is not proof of them.

## Operation and application-consumer map

All 79 OpenAPI operations are listed below, including aliases and static assets.
`api` means `internal/server/api`; configuration writes now delegate to
`management.Configuration`. Source handlers remain responsible for HTTP auth,
input/output and existing masking rules. The endpoint manifest remains OpenAPI;
this table is a reviewed mapping, not an independently executable API registry.

| Method | Path | Authentication | Business / representation entry | Consumer |
|---|---|---|---|---|
| GET | `/healthz` | Public | `server.NewEngine health handler` | Health probe |
| GET | `/metrics` | Public | `metrics.Render via server handler` | Prometheus / operator |
| GET | `/v1/models` | CallBearer | `gateway.ListModels` | API clients / model discovery |
| POST | `/v1/chat/completions` | CallBearer | `compat/generationhttp.HandleChatCompletions -> public Executor` | API clients / console playground |
| POST | `/v1/embeddings` | CallBearer | `compat/generationhttp.HandleEmbeddings -> public Executor` | API clients / real API acceptance |
| POST | `/api/auth/login` | Public | `api.Login` | Console sign-in/sign-out |
| POST | `/api/auth/logout` | SessionBearer | `api.Logout` | Console sign-in/sign-out |
| GET | `/api/me` | SessionBearer | `api.current authenticated User` | Owner console: overview / keys / models / logs |
| GET | `/api/me/keys` | SessionBearer | `api.MyKeys` | Owner console: overview / keys / models / logs |
| POST | `/api/me/keys` | SessionBearer | `api.CreateMyKey` | Owner console: overview / keys / models / logs |
| GET | `/api/me/keys/{id}` | SessionBearer | `api.MyKey` | Owner console: overview / keys / models / logs |
| PUT | `/api/me/keys/{id}` | SessionBearer | `api.UpdateMyKey` | Owner console: overview / keys / models / logs |
| DELETE | `/api/me/keys/{id}` | SessionBearer | `api.DeleteMyKey` | Owner console: overview / keys / models / logs |
| GET | `/api/me/models` | SessionBearer | `api.MyModels` | Owner console: overview / keys / models / logs |
| GET | `/api/me/logs` | SessionBearer | `api.MyLogs` | Owner console: overview / keys / models / logs |
| GET | `/api/me/usage` | SessionBearer | `api.MyUsage` | Owner console: overview / keys / models / logs |
| GET | `/api/admin/users` | SessionBearer | `api.Users` | Administrator users |
| POST | `/api/admin/users` | SessionBearer | `api.CreateUser` | Administrator users |
| GET | `/api/admin/users/{id}` | SessionBearer | `api.User` | Administrator users |
| PUT | `/api/admin/users/{id}` | SessionBearer | `api.UpdateUser` | Administrator users |
| GET | `/api/admin/keys` | SessionBearer | `api.ListTokens` | Administrator Key management / claim client |
| POST | `/api/admin/keys` | SessionBearer | `api.CreateToken` | Administrator Key management / claim client |
| PUT | `/api/admin/keys/{id}` | SessionBearer | `api.UpdateToken` | Administrator Key management / claim client |
| DELETE | `/api/admin/keys/{id}` | SessionBearer | `api.DeleteToken` | Administrator Key management / claim client |
| POST | `/api/admin/keys/{id}/claim` | SessionBearer | `api.ClaimKey` | Administrator Key management / claim client |
| GET | `/api/admin/model-policy` | SessionBearer | `api.SystemPolicy` | Administrator settings |
| PUT | `/api/admin/model-policy` | SessionBearer | `api.SetSystemPolicy` | Administrator settings |
| GET | `/api/admin/channels` | SessionBearer | `api.ListChannels` | Administrator channels / credentials |
| POST | `/api/admin/channels` | SessionBearer | `api.CreateChannel` | Administrator channels / credentials |
| GET | `/api/admin/channels/{id}` | SessionBearer | `api.GetChannel` | Administrator channels / credentials |
| PUT | `/api/admin/channels/{id}` | SessionBearer | `api.UpdateChannel` | Administrator channels / credentials |
| DELETE | `/api/admin/channels/{id}` | SessionBearer | `api.DeleteChannel` | Administrator channels / credentials |
| GET | `/api/admin/channels/{id}/credentials` | SessionBearer | `api.ListCredentials` | Administrator channels / credentials |
| POST | `/api/admin/channels/{id}/credentials` | SessionBearer | `api.CreateCredential` | Administrator channels / credentials |
| PUT | `/api/admin/credentials/{id}` | SessionBearer | `api.UpdateCredential` | Administrator channels / credentials |
| DELETE | `/api/admin/credentials/{id}` | SessionBearer | `api.DeleteCredential` | Administrator channels / credentials |
| POST | `/api/admin/credentials/{id}/status` | SessionBearer | `api.SetCredentialStatus` | Administrator channels / credentials |
| GET | `/api/admin/logs` | SessionBearer | `api.ListLogs` | Administrator API client / diagnostics |
| GET | `/api/admin/stats/summary` | SessionBearer | `api.StatsSummary` | Administrator API client / diagnostics |
| GET | `/api/admin/stats/by-model` | SessionBearer | `api.StatsByModel` | Administrator API client / diagnostics |
| GET | `/api/admin/stats/by-channel` | SessionBearer | `api.StatsByChannel` | Administrator API client / diagnostics |
| GET | `/api/admin/prices` | SessionBearer | `api.ListPrices` | Administrator settings |
| PUT | `/api/admin/prices/{model}` | SessionBearer | `api.UpsertPrice` | Administrator settings |
| DELETE | `/api/admin/prices/{model}` | SessionBearer | `api.DeletePrice` | Administrator settings |
| PUT | `/api/admin/group-rates` | SessionBearer | `api.SetGroupRates` | Administrator settings |
| GET | `/api/admin/abilities` | SessionBearer | `api.ListAbilities` | Administrator API client / diagnostics |
| GET | `/api/channels` | AdminKey / LegacyAdminBearer | `api.ListChannels` | Deprecated management client (same use case) |
| POST | `/api/channels` | AdminKey / LegacyAdminBearer | `api.CreateChannel` | Deprecated management client (same use case) |
| GET | `/api/channels/{id}` | AdminKey / LegacyAdminBearer | `api.GetChannel` | Deprecated management client (same use case) |
| PUT | `/api/channels/{id}` | AdminKey / LegacyAdminBearer | `api.UpdateChannel` | Deprecated management client (same use case) |
| DELETE | `/api/channels/{id}` | AdminKey / LegacyAdminBearer | `api.DeleteChannel` | Deprecated management client (same use case) |
| GET | `/api/channels/{id}/credentials` | AdminKey / LegacyAdminBearer | `api.ListCredentials` | Deprecated management client (same use case) |
| POST | `/api/channels/{id}/credentials` | AdminKey / LegacyAdminBearer | `api.CreateCredential` | Deprecated management client (same use case) |
| PUT | `/api/credentials/{id}` | AdminKey / LegacyAdminBearer | `api.UpdateCredential` | Deprecated management client (same use case) |
| DELETE | `/api/credentials/{id}` | AdminKey / LegacyAdminBearer | `api.DeleteCredential` | Deprecated management client (same use case) |
| POST | `/api/credentials/{id}/status` | AdminKey / LegacyAdminBearer | `api.SetCredentialStatus` | Deprecated management client (same use case) |
| GET | `/api/logs` | AdminKey / LegacyAdminBearer | `api.ListLogs` | Deprecated management client (same use case) |
| GET | `/api/stats/summary` | AdminKey / LegacyAdminBearer | `api.StatsSummary` | Deprecated management client (same use case) |
| GET | `/api/stats/by-model` | AdminKey / LegacyAdminBearer | `api.StatsByModel` | Deprecated management client (same use case) |
| GET | `/api/stats/by-channel` | AdminKey / LegacyAdminBearer | `api.StatsByChannel` | Deprecated management client (same use case) |
| GET | `/api/prices` | AdminKey / LegacyAdminBearer | `api.ListPrices` | Deprecated management client (same use case) |
| PUT | `/api/prices/{model}` | AdminKey / LegacyAdminBearer | `api.UpsertPrice` | Deprecated management client (same use case) |
| DELETE | `/api/prices/{model}` | AdminKey / LegacyAdminBearer | `api.DeletePrice` | Deprecated management client (same use case) |
| PUT | `/api/group-rates` | AdminKey / LegacyAdminBearer | `api.SetGroupRates` | Deprecated management client (same use case) |
| GET | `/api/abilities` | AdminKey / LegacyAdminBearer | `api.ListAbilities` | Deprecated management client (same use case) |
| GET | `/api/tokens` | AdminKey / LegacyAdminBearer | `api.ListTokens` | Deprecated management client (same use case) |
| POST | `/api/tokens` | AdminKey / LegacyAdminBearer | `api.CreateToken` | Deprecated management client (same use case) |
| PUT | `/api/tokens/{id}` | AdminKey / LegacyAdminBearer | `api.UpdateToken` | Deprecated management client (same use case) |
| DELETE | `/api/tokens/{id}` | AdminKey / LegacyAdminBearer | `api.DeleteToken` | Deprecated management client (same use case) |
| POST | `/api/tokens/{id}/quota` | AdminKey / LegacyAdminBearer | `api.SetTokenQuota` | Deprecated management client (same use case) |
| GET | `/api/admin/config-status` | SessionBearer | `api.ConfigurationStatus` | Administrator settings |
| POST | `/api/admin/config-status/apply` | SessionBearer | `api.ApplyConfiguration` | Administrator settings |
| GET | `/` | Public | `web.ServeHTTP` | Browser shell / ES Modules |
| HEAD | `/` | Public | `web.ServeHTTP` | Browser shell / ES Modules |
| GET | `/ui/{asset}` | Public | `web.ServeHTTP` | Browser shell / ES Modules |
| HEAD | `/ui/{asset}` | Public | `web.ServeHTTP` | Browser shell / ES Modules |
| GET | `/api/admin/billing/reservations` | SessionBearer | `api.BillingReservations` | Administrator billing console |
| GET | `/api/admin/billing/reservations/{id}` | SessionBearer | `api.BillingReservation` | Administrator billing console |
| POST | `/api/admin/billing/reservations/{id}/resolve` | SessionBearer | `api.ResolveBillingReservation` | Administrator billing console |

## Extension and lifetime acceptance

- Protocol registration uses `gateway.Options.Protocols/Operations` with neutral
  operation/parser and protocol Adapter registrations; the L2 protocol factory can adapt an existing
  `adaptor.Adaptor`, optional operation capability and per-attempt stream session.
  Supply a nonempty stable registry name, valid configuration and a non-nil
  implementation. Constructor copies versioned registration values; codec instances must be concurrency safe.
- Stateful multi-event codecs use `SessionStreamer.NewStreamSession` for every
  attempt; production decoding supplies this session. Anthropic-to-OpenAI uses
  the OpenAI output builder. The old stateless Anthropic parse/build helpers do
  not provide a native Messages streaming product. Anthropic streams allow at
  most 1,024 total blocks and 64 KiB of total tool-ID bytes per stream; closed
  block identities retain their small bookkeeping until stream end to reject
  reuse. Parameter text is forwarded, not accumulated in that state.
- `TestRegisteredAdaptorIsCopiedAndUsed` already exercises an injected adaptor
  through real gateway dispatch and isolates later map edits. S6 adds invalid
  injection/output behavior. The optional billing interfaces retain their
  current nil/fail-closed rules; admission/limiter invalid injection is rejected.
- There is no dynamic loader or public plugin SDK. An extension owns its
  resources until the application root stops/joins it; it must not start global
  workers, open a separate ledger, or retain request body/Key data in logs.
- UI extensions consume the current application API; existing page navigation
  and live-server browser consumers prove that contract. Use the existing
  ES Modules delivery. A new page does not bypass server authorization.
- Core is standard-library-only; WS libraries remain in transport; protocol
  cannot import business; gateway cannot import server/store; management can
  consume Store/model but cannot import Gin or a database implementation.

## Evidence and final gate

Applicable A1-A4 source and precise historical verification are recorded in
[the foundation audit](foundation-contracts.md) and
[implementation history](../history/implementation-s5-a3.md). Reuse applies only
to unchanged behavior and explicitly recorded scope. The S6 history records
the local concentrated run and fixes, separately from final CI and merge evidence.

The late local run covered changed package behavior, existing full checks,
the real temporary API/console chain and desktop/mobile layouts. The exact
feature head passed push [39062579429](https://github.com/ANRCM0/TXAPI/actions/runs/39062579429)
and PR [39063298294](https://github.com/ANRCM0/TXAPI/actions/runs/39063298294)
complete CI on latest fetched baseline `758ad04`.
[PR #8](https://github.com/ANRCM0/TXAPI/pull/8) then merged that verified head
into `ca009c2a1e3aa00bfb978f9d9e48785db979763a`; merged-master
[39063693685](https://github.com/ANRCM0/TXAPI/actions/runs/39063693685) also
passed complete CI. Both `check` and `delivery` passed in each run, without
manual dispatch or admin/bypass. Exact commands, failures, environment,
requirement-by-requirement scope and acceptance are in
[the S6 record](../history/implementation-s6.md#2026-10-18-s6-framework-acceptance-and-m1-baseline).
These runs certify the named source, not later documentation text. M1 is the
accepted current compatibility framework, not unresolved S5 cost proof, new
strict permission or S7 product acceptance.
