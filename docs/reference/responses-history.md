# Responses Local History And Conversations

Status: S7-A02 working implementation on `feat/s7-a02-persistent-conversations`,
based on accepted `master/04fbfa62aad2ad6c3bbc2b4a153b634095c0b6a0`.
This is not matching remote CI/merge acceptance. A02 and M2 remain unchecked
in [PLAN](../PLAN.md). See [batch evidence](../history/implementation-s7-a02.md).
The accepted [A01 foreground contract](responses-http.md) remains the basis;
this batch explicitly expands its former retrieval/continuation exclusions.

## Supported API

All routes require the currently valid **creating API key and its user**.
Administrator sessions and another key of the same user cannot read or mutate
content. Missing, expired, deleted, wrong-owner/key and model-no-longer-authorized
content returns the same 404. Revoked credentials return 401. There is no new
administrator content-reading bypass or user-supplied owner field.

| Method | Route | Behavior |
|---|---|---|
| GET | `/v1/responses/{response_id}` | Complete locally stored native response object, including native usage/terminal fields |
| DELETE | `/v1/responses/{response_id}` | Delete this response's encrypted snapshot and private replay; preserve minimal receipt and financial facts |
| GET | `/v1/responses/{response_id}/input_items` | Actual expanded input items for this generation |
| POST | `/v1/conversations` | Create local conversation, optional metadata and up to 20 initial text/function/reasoning items |
| GET | `/v1/conversations/{conversation_id}` | ID, object, created_at and metadata |
| POST | `/v1/conversations/{conversation_id}` | Replace metadata; maximum 16 string pairs, 64-character keys and 512-character values |
| DELETE | `/v1/conversations/{conversation_id}` | Hide the conversation; its encrypted items remain retained until its fixed TTL |
| GET | `/v1/conversations/{conversation_id}/items` | Paginate retained items |
| POST | `/v1/conversations/{conversation_id}/items` | Atomically append 1-20 supported items |
| GET | `/v1/conversations/{conversation_id}/items/{item_id}` | Read an owned item |
| DELETE | `/v1/conversations/{conversation_id}/items/{item_id}` | Delete the item; return the updated conversation object |

There is no official single-item update route in the schema used by this batch;
metadata replacement and item append/delete are the implemented mutations.
Media, hosted tools, item references, native background and WS remain unsupported.

Lists return `object`, `data`, `first_id`, `last_id` and `has_more`. `limit` is
1-100, default 20; `after` must identify an item in the current owned snapshot.
The declared local `order` option accepts asc/desc, default asc. Unknown query
switches, duplicate scalar parameters, unknown request fields and ambiguous JSON
fail explicitly. This batch does not fetch missing provider-only `include` data,
stream GET results or provide background event cursors. Existing retained opaque
reasoning bytes are preserved without interpretation or invented content.

## One Context Source Per Generation

`POST /v1/responses` accepts either `previous_response_id` or `conversation`
(a local ID string or an object with `id`), never both. The application loads
and authenticates the complete local snapshot, appends the new input and sends
the expanded native input through the existing `Gateway.ExecuteTo` chain.
It removes both reference fields from upstream wire JSON. Native upstream
response IDs remain private provenance in receipts, not client authority.
An expired or unknown provider-only parent ID is not looked up or sent.

This is an explicit local-history implementation, not a fallback from failed
native continuation. It never sends full local history together with a native
parent ID and never drops opaque encrypted reasoning or function-call IDs.
Local input-item IDs generated for list APIs are removed from upstream wire
objects; native reasoning/function item IDs and `call_id` associations remain.
Instructions/tools and other options belong to the current generation request;
they are not silently inherited from the previous request.

Every new generation independently reauthorizes, enters existing RPM/TPM and
concurrency admission, uses a fresh selling-price snapshot, executes and settles.
The encrypted snapshot records the effective request, full input/output,
parent/conversation relationship, original target binding and price/billing
provenance. Legacy missing-usage estimates see expanded input; they are not a
strict proof. **All strict Responses remain refused**, as in A01.

Continuation requires the same public model and a current capable target with
the original channel, credential, mapped model and fingerprint of provider type,
base URL and key material. A changed mapping/key/URL, deleted target or unavailable
credential cannot cause silent account switching. There is no automatic
cross-provider reconstruction or retry after unknown execution.

Each stored response owns an independent flattened snapshot. Siblings branch
from their parent snapshot; one sibling cannot append to another. Deleting or
expiring a parent does not remove independent child/conversation copies. Deletion
affects local storage, not the provider's own `store` retention or old backups.

## Concurrency And Unknown Results

Responses and Conversations use monotonic local revisions. History reads return
an ETag; optional `If-Match` rejects stale metadata/item/delete mutations with
409. SQLite CAS is enforced even without that header. Pages describe the current
snapshot, not an immortal pagination snapshot across concurrent mutations.

A conversation's generation slot and the new pending response snapshot are
committed in one bounded transaction before upstream execution. A competing
generation, append, metadata update or active delete returns 409. Completion
atomically publishes the response snapshot and appends input/output to its
conversation. It never holds a database transaction while waiting for network I/O.
The first generation admission binds the conversation to its public model.

A request rejected before dispatch releases the local conversation slot without
appending new items. After a partial write, cancellation, panic, persistence
failure or interrupted restart, unknown execution cannot be automatically reused.
Startup marks abandoned slots uncertain before producers start. The last
committed conversation prefix remains readable, but no new generation may append
over the missing outcome. Its owner may explicitly delete an uncertain local
conversation; this does not cancel an upstream operation, release money or erase
the durable idempotency receipt. TTL may remove historical content but never
turn an unknown receipt into permission to re-execute.

JSON/SSE still delivers synchronously through A01 archive-first output. History
publication occurs after native completion and gateway financial finalization;
a caller may have received output before the final snapshot write fails. Such
a failure blocks history/automatic regeneration rather than claiming an empty
successful stored response. GET does not regenerate or poll the provider.

## Encryption, Limits And Retention

Schema v12 adds `response_histories`; it does not infer history from old v11
receipts, output caches or request logs. Old receipts are not retroactively
upgraded into public stored responses. Financial tables and released migrations
remain unchanged. Older binaries must refuse schema v12; stopped coherent
backup/restore is the rollback path.

History uses the same mounted content-key file and standard-library AEAD ring
as the existing runtime, with a distinct history domain. Identity, owner/key,
kind/model, revision, creation/expiry, reservation and deletion state are
authenticated. Metadata, effective requests, items, response objects and target
fingerprints are encrypted. No input, output or raw calling key belongs in
request logs or the new plaintext columns. Required retained keys are included
in startup key-requirement checks. Retain old keys until actual cleanup, including
deleted conversations and expired-but-not-yet-cleaned rows.

| Bound | Default / fixed maximum |
|---|---|
| `responses.history_seconds` | 86400; configurable 60 seconds to 7 days, must cover generation timeout plus finalization |
| `responses.history_instance_bytes` | 64 MiB reservation budget, configurable up to 1 GiB |
| `responses.history_owner_bytes` | 16 MiB reservation budget, minimum 2 MiB |
| History object counts | Existing `instance_records` / `owner_records`, separately enforced for history rows |
| Encrypted snapshot | At most 2 MiB; a pending response or conversation reserves this full capacity |
| Expanded stored request | At most 256 KiB and 1024 total input items; refusal before execution, no silent trimming |
| History read/mutation rate | 120 per owner per minute; at most 4096 rate windows, expired windows evicted at capacity |
| Active delivery | Existing finite Responses instance/owner limits, no unbounded waiting queue |
| Cleanup | Fixed TTL from each object's creation, bounded 200-row batches, joined by the existing root lifecycle |

Byte quotas are logical ciphertext reservations, not a physical SQLite/WAL/disk
quota or secure-erasure promise. Response deletion leaves a bounded tombstone
until TTL, so object-count capacity is not immediately recycled. Conversations
may outlive response receipts when configured with a longer TTL; deletion does
not depend on an already-expired receipt still existing. Shared A01 replay quotas
and its short TTL remain separate. A large/failing output can make history
unavailable after irreversible provider cost; no plaintext fallback or fabricated
complete history is allowed.

`store:false` makes no persistent history record or output archive and cannot
reference a stored parent/conversation. It retains only A01 minimal receipts;
a repeated completed request cannot retrieve or regenerate its body. Normal
stored reads and mutations do not consume generation RPM/TPM, start attempts
or create a new charge; they have their own bounded delivery rate/capacity.

## Official Sources And Acceptance Boundary

Official pages were fetched on 2026-10-18, including the actual endpoint examples:

- [Response retrieval](https://developers.openai.com/api/reference/resources/responses/methods/retrieve)
- [Response deletion](https://developers.openai.com/api/reference/resources/responses/methods/delete)
- [Input items](https://developers.openai.com/api/reference/resources/responses/subresources/input_items/methods/list)
- [Create conversation](https://developers.openai.com/api/reference/resources/conversations/methods/create)
- [Update conversation](https://developers.openai.com/api/reference/resources/conversations/methods/update)
- [Delete conversation](https://developers.openai.com/api/reference/resources/conversations/methods/delete)
- [Conversation items](https://developers.openai.com/api/reference/resources/conversations/subresources/items/methods/list)

The HTTP shape is a declared supported subset with local state/limits, not a
claim of every vendor feature or official SDK/Codex certification. Tests use
isolated temporary SQLite and loopback providers only. A03 native background,
A04 WS, A05/A06 cross-mode/client/real-provider acceptance and S5 cost proof
remain separate; working implementation and local tests do not close A02/M2
before matching complete remote CI and latest-base merge acceptance.
