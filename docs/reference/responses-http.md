# Responses HTTP/SSE foreground contract

Status (2026-10-18): S7-A01 accepted and merged by PR #10. Final feature head `3195d09` on base `bea97fa` and actual merged `master/a925140` passed their own complete automatic push/PR/master CI, including Linux race and container delivery. See [exact acceptance evidence](../history/implementation-s7-a01.md#2026-10-18-s7-a01-functional-acceptance), including the failed initial delivery run and repair. This extends, rather than reopens, the accepted [S6 contracts](compatibility-contracts.md). Responses remains opt-in. A02/M2 was subsequently accepted separately through PR #12; A03-A06 remain incomplete.

## A02 Accepted Extension

A02 separately implements local encrypted response/history and Conversations
APIs with full-context continuation. Its 0.4.0 / 91-operation contract was accepted
through PR #12 / `master/765501a`: final head `2084bcd` and merged master passed
their own complete push/PR/master CI. See [history contract](responses-history.md)
and [A02 evidence](../history/implementation-s7-a02.md). The A01 exclusions below
describe its original scope; current history support follows the A02 contract.

## FC-002 Accepted Background Extension

A03-01/02/03 were accepted in order through PR #20/#21/#22. A03-03 feature
`501507d` and merged `master/a44d4d1` passed complete push/PR/master CI, including
Linux race, actual-main smoke and schema-v15 container delivery. OpenAPI 0.5.0 /
92 operations adds the opt-in disabled-billing-only self-contained native background
chain, original stream=true event persistence/replay and cancel/terminal delete.
The two additional versioned entries are frozen only for that accepted subset.
See [background runtime](responses-background.md), [DoD](foundation-integration-v1.md#fc-002-追加公开入口冻结)
and [exact evidence](../history/implementation-fc002.md#fc-002-最终验收与冻结).
The foreground sections retain their original A01/A02 semantics; this extension
is not full A03/M3, charged background, parent composition, native WS or SDK/provider certification.

## Supported Surface

`POST /v1/responses` uses API-key authentication and the existing gateway generation/attempt/admission/finalization chain. The native OpenAI-compatible adaptor handles text, instructions, function tools, function_call/function_call_output history and opaque reasoning items without Chat conversion. JSON and native SSE preserve output items, usage details and completed/incomplete/failed/cancelled semantics. A native failure can follow HTTP 200; it is not rewritten to a fabricated success or Chat `[DONE]`. Provider error details are sanitized. Other upstream protocols are not bridged through Chat.

Responses is opt-in. An authenticated request returns 503 when disabled; anonymous requests remain 401. Strict billing rejects Responses with 400 `unsupported_billing_dimension` before admission/reservation/attempt. Disabled and legacy billing use the existing ports; legacy estimates are not a strict cost guarantee. Existing Chat/embeddings routes and middleware remain unchanged.

A01 originally excluded public retrieval/deletion, previous_response_id and conversation; A02 now supplies these through the local-history contract above. Item references, background=true, hosted tools, media, sessions and WebSocket remain unsupported. Provider-side `store` is preserved: omitted means the provider default, not an injected `false`. Native provider state is not automatically local conversation state.

## Request Identity And Deduplication

The tracing request ID, generation ID, local public response ID and upstream response ID are separate facts. Each fresh generation allocates a random 64-hex generation ID and `resp_` public ID. The decoder rewrites only response identity fields, never item IDs; original upstream response ID remains bounded metadata. Response headers expose `X-TXAPI-Generation-ID`, `X-TXAPI-Response-ID` and `X-TXAPI-Idempotency` (`created` or `replayed`). They are exposed to browser callers by CORS. Neither tracing IDs nor upstream IDs authorize access.

Optional `Idempotency-Key` contains 1-256 visible ASCII bytes. The raw key is never persisted or forwarded to the provider. A stable external 32-byte digest key produces domain-separated HMACs for the owner/Key scope and request. Canonical JSON sorts object keys and normalizes whitespace/escaping, while preserving numeric lexical forms, array order and omitted fields; duplicate keys, invalid UTF-8 and excessive depth are rejected. Explicit/defaulted representations are not silently equated.

A SQLite writer transaction revalidates owner/Key/system model authority, checks receipt quotas and inserts `dispatching` before gateway execution. The unique scope is `(user_id, token_id, operation, key_hash)`. Same key and different canonical request returns 409 `idempotency_conflict`; concurrent same request returns 409 `idempotency_in_progress` with Retry-After. Fresh execution alone enters generation RPM/TPM, scheduler and billing. Replays revalidate current identity/model and perform no attempts or new charges. IP and bounded application delivery limits still apply.

Final receipt updates compare immutable identity and dispatching state. A complete native failed/cancelled event can be sealed and replayed without re-execution. Downstream/archive failures and panics are not replayable. Startup changes abandoned dispatching receipts to uncertain before any producers start. Unknown results return 409 `idempotency_unknown` and are NEVER automatically retried, evicted or expired into a new execution. Retained unknowns can fill bounded quotas and require later owner-owned operational reconciliation; do not delete them to force a retry.

Known terminal receipts expire after the configured metadata retention window (default 24h). Reusing a key after this window is a NEW generation and can charge again. Without an Idempotency-Key, separate POSTs are separate generations.

## Private Replay Cache And Privacy

For `store:true` or omitted store, encoded output metadata/body is encrypted through the existing Archive BEFORE it is forwarded to the current observer. Complete stored output may be replayed byte-for-byte during the configured short cache lifetime (default 1h). The application owns synchronous execution; this is not an asynchronous worker, public history API, S7-A02 conversation database or provider idempotency promise. No request input body is archived by this service.

`store:false` retains only bounded non-content idempotency metadata; no output archive is created. A completed repeat returns 409 `idempotency_result_unavailable` with stable identity headers, never silently regenerates. The same refusal applies when a stored body expires, exceeds the archive limit, fails I/O or cannot be authenticated. An irreversible send may still have provider cost even if the client receives only a partial response or no body. Droppable access logs are not recovery authority.

The encrypted archive shares existing owner/instance record and ciphertext-byte quotas with durable generation records. Default Responses ceilings are 1024 instance/128 owner receipts, 16 MiB instance/4 MiB owner archive bytes, and 256 KiB per encoded archive (including encrypted segments/metadata). A record also has a finite 8192-segment ceiling (including metadata and final result); small SSE events are no longer limited by the earlier 256-segment foundation bound. Active request/read deliveries are bounded to 32 instance/4 per owner, with no unbounded waiting queue. Input is at most 1 MiB. Unsupported capacity returns 429 or a safe archive-unavailable response, not a plaintext fallback. Failed/expired private bodies may be cleaned while uncertain receipt identity remains protected.

## Configuration, Keys And Lifecycle

`responses.enabled` defaults false. See [example config](../../configs/gateway.example.yaml). Enabling requires `database.credential_keys_file` and activated credential encryption, plus `responses.keys_file` or `GATEWAY_RESPONSES_KEYS_FILE`. The content key file uses the existing generation key JSON format (`active_key`, `keys`, `digest_key`); key bytes never belong in app YAML or Git. Both runtimes must use the same content-key file when enabled together.

Startup verifies retained ciphertext key requirements and the immutable `responses.idempotency_digest_version` marker. Missing/incompatible retained keys, a changed digest key or a missing marker with existing receipts fail closed. Retain old encryption keys for retained output; never rotate the digest key in place. Follow [secret storage and stopped restore](secret-storage.md); old backups/WAL/memory and provider storage remain distinct concerns. Do not operate two gateway processes on the same database: startup recovery assumes exclusive ownership.

Replay retention must cover the configured total request timeout plus five seconds of finalization; expiry is measured from admission. The composition root starts Responses recovery before HTTP or durable workers, owns the bounded retention ticker and joins it after HTTP draining and before closing SQLite. No goroutine or second ledger implements foreground inference. The schema is v11; migration adds an empty receipt authority and does not infer prior generations from request logs or change existing holds/balances.

## Background Extension Boundary

A future **charged** Responses background consumer must atomically commit its immutable execution intent and BOTH financial holds through the existing intent admission transaction before acknowledging acceptance. The current `generation.IntentAcceptor` / `IntentAdmission` port is real for Chat/embeddings and preserves rollback and CAS behavior; `ExecuteClaimedTo` explicitly refuses Responses until its operation metering and durable payload semantics are added. A foreground `ResponseRequest` receipt is NOT a runnable intent and cannot authorize worker execution or reserve/release strict holds. S7-A03 must extend the owner-owned transaction, not append a queue write after reserving money or reuse this receipt as a second task authority.

## Source And Acceptance Scope

Native semantics were checked against the [official OpenAI Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create) and [API request-ID documentation](https://developers.openai.com/api/reference/overview) on 2026-10-18. X-Client-Request-Id is tracing, not a verified deduplication guarantee. This implementation relies on its own persisted receipt and never promises exactly-once provider execution after unknown network outcomes. Local test providers are isolated fixtures; no real account, real provider cost, SDK/Codex client or production database acceptance is implied. S5-03 and S7-A03-A06 remain independent; A02 has its own acceptance evidence above.


FC-001 FND-04 preserves this accepted HTTP/persistence contract through the new
`compat/responseshttp` entry and the neutral `responses/v1` Begin/Call/Command/Delivery
port. The application does not parse URL/HTTP headers or construct writers; L2
maps native documents, SSE/provenance and conditional metadata. Current scope,
resource ownership, frozen entry points and completed acceptance are in
[Foundation API v1](foundation-api-v1.md#fnd-04-应用与表示) and
[FC-001 implementation evidence](../history/implementation-fc001.md).
