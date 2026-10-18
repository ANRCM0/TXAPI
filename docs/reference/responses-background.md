# Native Background Responses

Status: 2026-10-18, A03-01/02/03 accepted in order. A03-03 feature `501507d`,
PR #22 and merged `master/a44d4d1` passed complete automatic push/PR/master CI.
The two additional entries background/v1 (Version=1) and responses/v2 (Version=2)
are frozen only for the accepted subset; the original fourteen entries and L0
remain unchanged. See [exact evidence](../history/implementation-fc002.md#fc-002-最终验收与冻结)
and [seven-part DoD](foundation-integration-v1.md#fc-002-追加公开入口冻结).
This is only the disabled-billing, self-contained slice, not full A03/M3, charged
background, a Responses cost proof, native WS, or real-provider/SDK certification.

## Enable Explicitly

Start from [the configuration example](../../configs/gateway.example.yaml).
Activate credential encryption through the existing stop/backup/key procedure,
mount the persistent content/digest key file, and set:

```yaml
billing:
  mode: disabled
generation:
  enabled: false
responses:
  enabled: true
  keys_file: /run/secrets/txapi-generation.json
  background:
    enabled: true
    channel_ids: [1]
    retention_seconds: 600
    metadata_seconds: 86400
```

`channel_ids` are actual enabled database channel IDs, not seed-list indexes.
Each opted-in channel must use a verified native Responses background provider;
the `openai` protocol label alone never asserts this capability. Main explicitly
registers the independent parser/codec. Missing, duplicate, incompatible or typed
nil registrations fail closed. The provider URL, API-key Bearer credential,
channel, credential ID and mapped model are pinned in the private binding.
Configuration publication cannot silently move existing work to another account.

Legacy/strict background enablement fails validation before serving. Outstanding
nonterminal/uncertain background work also prevents switching billing modes when
both Responses switches are off. Disabled means no gateway monetary charge, not
that the provider is free or its spend has been proved bounded.

## Current HTTP Surface

- `POST /v1/responses` with `background:true` durably accepts a self-contained
  text/function-tool input. It sends real native `background:true`, never a
  synchronous replacement or a Chat translation. A03-03 adds original
  `stream:true` observation/replay; previous_response_id, conversation, hosted tools and media fail
  before dispatch. The input plus private envelope must fit 256 KiB.
- The initial JSON response is a local queued receipt. HTTP 200/queued proves
  local durable acceptance, not remote acceptance, task completion or cost
  settlement. Native queued/in_progress observations stay nonterminal.
- `GET /v1/responses/{id}` reads the persisted local/native projection. It does
  not start another generation or expose the remote ID, target binding, claim,
  credential or database. An uncertain dispatch returns 409 background_unknown.
- `POST /v1/responses/{id}/cancel` first persists cancellation intent. A worker
  subsequently requests native cancellation on the existing binding. Its response
  may remain nonterminal; an already-terminal result is unchanged. Queued work
  can be locally cancelled before POST. This command does not cancel foreground
  A02 responses and does not accept If-Match.
- `DELETE /v1/responses/{id}` deletes **only known terminal** local background
  content. Active/uncertain or stale If-Match revisions return 409. It does not
  cancel or delete a provider resource. Background input_items and background
  continuation/history composition are not enabled.

The accepted [OpenAPI](../openapi.json) is 0.5.0 / 92 operations. The prior
accepted A02 version is 0.4.0 / 91 operations. Original foreground JSON/SSE,
history/Conversations, query rejection and the frozen responses/v1 facade retain
their behavior; only the separately registered v2 facade accepts cancellation.

For background GET, stream and starting_after are parsed separately. Duplicate
or unknown query names, malformed booleans, negative/overflow cursors, noncanonical
integer text and starting_after without stream=true return 400. Event recovery
requires an original stream=true job. A missing cursor includes sequence zero;
explicit starting_after=0 skips zero. Future/missing/conflicting/unavailable
sequences fail explicitly with 409 before SSE commit. Ordinary foreground GET
retains its prior query rejection, and non-stream jobs cannot invent events.

## Native Event Recovery (A03-03 Accepted)

POST background=true,stream=true observes durable native events, not a fabricated
local queued/created SSE event. GET stream=true observes/replays the same event
log. The pure L2 codec validates typed nonnegative signed-64-bit sequence_number,
native identity and event type/status before the owner can publish bytes. It
rewrites only documented response identity/error envelope positions and retains
native text/function-tool deltas and extension fields. SSE transport comments,
id and retry fields are not replayed; validated JSON is canonically encoded.
There is no fabricated Chat [DONE]. Standalone error events without a complete
native terminal response are outside this subset and make replay unavailable;
response.failed with its full native response is supported.

Each event and its remote binding/snapshot commit in the same fenced transaction
before output acknowledgement. Identical canonical plaintext at an overlapping
sequence reuses the original ciphertext; changed content never overwrites it.
Sequences must be consecutive from zero. Limits are 64 KiB per encoded event,
128 events per transaction, 8192 events and the existing total content reservation
per task. Public pages are at most 64 KiB/128 events (default 64); the completion
flag applies only to a page that has reached the durable terminal event.

A native JSON cancellation/poll terminal result can end the task and free its
active quota while its event tail is still incomplete. Private ReplayPending
keeps only that event-recovery obligation claimable. Historical queued/running
events cannot regress the latest terminal document, and matching late usage
cannot adjust TPM twice. Delete or fixed content expiry fences replay and clears
this obligation. An explicitly unrecoverable log is not marked complete: if its
task is still active, JSON inspection/cancellation cleanup continues. Known
terminal state does not regress to active or uncertain merely because events
expire. A lost-ID dispatch remains protected uncertain and is never re-POSTed.

A clean RPC EOF is not native task completion. An incomplete final SSE frame is
not committed; reconnect resumes it after the last durable sequence. Transient
network interruption resumes GET, never POST. Unsupported resume, invalid native
events, gaps, corrupt overlap and exhausted event capacity become explicit replay
unavailability rather than silent truncation or a fabricated successful stream.

An observer has at most five minutes, bounded by content expiry, and uses the
existing per-owner read quota (120 reads/minute) and delivery capacity. It polls
at one second and reauthorizes every page. Multiple observers share that quota;
reaching it closes observation without cancelling the task. Each write has a
10-second deadline; short writes/errors stop the observer without changing the
persisted task. Before commit errors use ordinary JSON; after SSE commit failures
only close the connection, with no appended JSON/error/DONE pretending completion.
Temporary background SSE explicitly uses Cache-Control: no-store.

## One Resource And Idempotency Authority

Every content/classification/mutation checks the current user, creating Key and
current model permission; a public ID or copied identity projection grants no
access. Cross-owner/key, deleted, expired and inaccessible resources are
non-enumerable. Administrators do not bypass content ownership.

A single Store read transaction positively classifies known resources. HTTP/L3
never guesses the owner by trying a background read and then falling back to an
A02 history read on ErrNotFound. Classification itself does not grant a later
read: the selected owner rechecks authorization in its own transaction.

Idempotency-Key is current-owner/creating-Key scoped, 1-256 visible ASCII bytes,
never forwarded or stored plaintext. Both directions of foreground/background
key reuse conflict before new provider I/O. The v13 background key digest stays
unchanged; v14 adds a compatible shared-namespace HMAC. Retained v13 rows without
that hash conservatively block new foreground admissions for their owner/Key
until an identical authenticated background replay backfills it or the protected
metadata expires. The original ciphertext, task ID, deadline and revision do not
change during backfill. HMAC key changes cannot create another namespace.

Terminal deletion clears encrypted input, document, binding and events and
releases their content capacity, while retaining a non-content terminal/dedup
tombstone until its original metadata expiry. Repeating the accepted key cannot
revive the job: it returns result unavailable. Unknown work never becomes safely
replayable because of disconnect, lease expiry, deletion or content expiry.

## Independent Lifetimes And Bounds

The fixed owner has two queue-less workers. It claims only when a worker is free;
there is no unbounded per-job goroutine or memory queue. Each control pass has a
30-second fenced lease and a 12-second enclosing context; core I/O is capped at
10 seconds, with at least one second between passes and bounded exponential
control-failure backoff. The task dispatch deadline is at most five minutes.
While its binding content remains live, a pass after the deadline requests
cancellation; expiry/unavailability still cannot fabricate remote termination.

Before the sole submit RPC, current permissions, original request/attempt
capacity and generation RPM/TPM admission are checked. A transaction persists
dispatching plus the exact target first. If POST might have happened but the
remote ID was not persisted, the job becomes uncertain and is never re-POSTed.
After the ID is durably bound, restart performs only inspect/cancel/resume on that
binding; credential removal/rotation or model/URL changes cannot switch targets.

Client disconnect stops only observation. Bound control uses its fenced private
claim and exact target, not a synthetic old principal. Revoked user/Key/model
permissions block content/new work while still permitting tightly scoped native
cancellation cleanup. Controls do not consume generation RPM/TPM or money again;
public reads share the existing bounded per-owner read rate and delivery capacity.
The initial TPM estimate is retained until native terminal usage adjusts it once
in the same runtime epoch. A restarted volatile limiter is never adjusted using
an old epoch; absent usage remains conservative. This is not a provider cost proof.

Default instance/owner limits are 16/2 active tasks, 1024/128 retained records and
64/16 MiB reserved encrypted content. Each task reserves its entire input/content
envelope up front (256 KiB input plus 2 MiB content and encryption overhead).
Native JSON documents are capped at 1 MiB. Core's existing network buffering is
separately bounded; runtime persistence cannot grow without the declared quota.
Unknown records continue to count against active/metadata limits. Storage/output
failures do not penalize provider credential health or trigger another POST.

Close cancels and joins owned RPC/workers before gateway, limiter or Store can be
closed. A context-expired Drainer.Close is not proof that dependencies are safe
to destroy; its owner must retry/join. Shutdown of a bound task leaves it bound
and nonterminal, not locally completed/cancelled. No remote-cancellation guarantee
is inferred from process shutdown.

## Temporary Retention, Keys And Rollback

**Background store=false still stores temporary encrypted input, binding and
recovery content. It is not zero storage, zero retention, A02 permanent history,
or a ZDR certification.** The local default content TTL is ten minutes, configurable
from five minutes to one hour; metadata defaults to 24 hours, at most seven days.
Reads, controls and retries never extend these deadlines. API responses expose
no-store, ETag, X-TXAPI-Background-Retention=temporary and the fixed local
X-TXAPI-Background-Expires-At; this is not a provider retention guarantee.

Content is inaccessible at its deadline. Startup and bounded periodic cleanup
clear expired ciphertext/events, including when the Responses/background product
switches are off or content keys are not loaded for that disabled feature.
Physical deletion can lag the access deadline by the bounded cleanup backlog or
storage failure; an offline process cannot sweep until restarted. Fixed privacy
retention is not subordinated to successful foreground maintenance. Unconfirmed
work retains only non-content uncertainty/namespace protection after content TTL;
it is explicitly unrecoverable and never silently freed for replay.

Unified v14 appended private deletion and namespace metadata to v13. Schema v15
adds private ReplayPending with a false default for old records; it does not
invent missing native events or alter released intent/ledger/history ciphertext
or AAD. Retained key
requirements include background input, snapshots and event key IDs. Wrong or
missing keys for live content fail closed. Keep the original external content
and digest key material and coherent database backups. Older binaries reject
the newer schema; turning a switch off is not an in-place binary downgrade.

Turning background off stops new product access and controls but not privacy
retention. For outstanding work, keep a compatible disabled-billing version with
the feature enabled if query/cancel recovery is required. Do not delete unknown
rows/tables, move tags, overwrite accepted facts with an old backup, or claim
that rollback ended remote execution. Reconciliation of lost-ID uncertainty and
charged tasks requires a separately approved owner-owned procedure.

## Validation Boundary

Tests use real temporary SQLite, the actual owner/gateway/core/HTTP facade and
isolated loopback native providers. They cover real background flags, queued
versus terminal facts, disconnect, retained bindings across reopen, current
revocation, exact-account controls, CAS deletion/tombstones, shared namespace,
mode guards, fixed retention, shutdown and original foreground regressions.
Preserve all prior tests. Commands, failures, fixes, exact source fingerprints
and final CI/merge status belong to the [batch record](../history/implementation-fc002.md).
No real provider account, spend, production data or external SDK acceptance is
implied by these fixtures.
