# R05a-3: Durable Frontend Invocation Usage Metadata

Decision recorded before implementation, 2026-10-19. Base is the tree-verified
R05a-2 merge master/b30bd5760bfd9522709cd84a20baa7c97da21b9f, identical to
accepted head f794dfa. Push39201855006 and PR39201860012 passed complete
check/delivery. Independent merged-main39202515426 is tracked separately.
Backend work remains first; frontend is deferred and the real OAuth gate stays
closed. Existing authorization permits the implementation choices recorded here.

## Six-Part Boundary

1. L1 gateway records accepted foreground execution through its existing
   coordinator/attempt path. Store owns two bounded metadata tables, append-only
   migration16, transactions, retention and current permission checks. Config/main
   own opt-in wiring and shutdown. No second executor, log queue or balance owner.
2. New candidate usagehistory/v1 values/Recorder/Repository/Service consume the
   existing v1 host facts, v3 quantity observation and quotation/v1 current
   metadata. L3 usageconsole consumes existing control/admission plus the new
   repository. The independent facts example consumes real framework ports.
3. L0, all existing registered declarations, price format1/2, ledger/budget
   behavior and ordinary disabled-feature execution stay exact. Only billing
   disabled may enable recording. Legacy/strict plus enabled recording fail
   configuration/construction. Background, durable claimed execution and native
   WS are explicitly excluded by internal invocation source, not guessed from
   billing mode. R05b charging remains closed.
4. Exact new candidate interfaces and data semantics are defined below and in
   the new contract source. Four session-authorized read-only HTTP operations
   append to the existing104 operations; no mutation, replay or frontend route.
5. Prove real JSON/SSE unknown/null/zero and retry model/group binding; all
   synchronous write failure points; idempotency/conflicts; process death and
   restart; current owner/admin isolation; bounded capacity/cleanup/close join;
   old migration/data/financial behavior. Finish concentrated local checks and
   exact-head complete CI, merged-tree and independent main checks.
6. Integration owns contract/registry/public snapshot, config/main, L3/API/
   OpenAPI and smoke/docs. Gateway agent owns gateway source/tests only. Store
   agent owns new model/store source, migration16/AutoMigrate registration and
   store tests, including reuse of the existing transaction session checker.
   Independent test agent owns its assigned new integration files only.

## Capability And Admission Policy

Root startup-only usage_history defaults disabled. Enabled requires billing
disabled. Finite defaults are4096 invocation records per instance,256 per owner
and604800 seconds retention; maxima65536 records and2592000 seconds. Each Begin
reserves the request's RetryTimes+1 attempt slots, maximum11, by storing that
immutable limit in its counted parent. No arbitrary additional child is allowed.
Logical row bounds do not claim a physical SQLite file/WAL space ceiling.

The recorded scope starts after current identity, parse/model authorization,
request concurrency and rate admission and before relay. Earlier rejections are
not promised a durable record. Begin allocates a server-generated usage_ plus
32 lowercase hexadecimal ID, independent of client X-Request-ID. The latter is
only a bounded optional correlation field; repeated client IDs never deduplicate
generation. Owner/key come from the trusted context and are revalidated in the
Begin database transaction. Body/tool/output/raw error/URL/header/key are never
stored. Current quotation version is captured once if enabled; an empty version
means unavailable, never a later latest fallback. It is not a charged price.

This opt-in is a new durable-record admission policy: Begin/Prepare/MayDispatch
failure rejects the affected unsent attempt with fixed usage_recording_unavailable
and does not dispatch it. Earlier attempts remain actual executions and retain
their own facts. The ordinary disabled-feature path is unchanged.

After an upstream execution, an Observe/Finish error preserves the actual
Outcome and already written response. It prevents any further upstream retry;
it never repeats generation, fabricates a final response, changes a hold or
converts unknown quantities to zero. A record ID header can be supplied after
Begin and before output commit; final recording state is queried, not inserted
into a response after commit. A lost database acknowledgement is uncertain to
the caller; the database may already contain the exact committed record. Only
the same canonical write may be retried to resolve that uncertainty.

## New Candidate Contract

Version="1"; fixed errors Invalid, Unauthorized, Forbidden, NotFound,
Unavailable, Conflict, Capacity and RateLimited. Names are ErrInvalid etc.

Start contains ID, RequestID, Model, QuotationVersion strings, Operation v1.Operation,
Stream bool and MaxAttempts int. Target contains ID, ActualModel, Group, Protocol
strings, Number int and ChannelID/CredentialID uint. HostFacts contains Phase
v1.Phase, Status/UpstreamStatus/HTTPStatus int, Termination v1.Termination,
ProtocolComplete/DeliveryCommitted bool and BytesWritten int64. Receipt contains
ID string, Number int, Host HostFacts and Usage v3.UsageObservation. Completion
contains ID string, Host HostFacts and Interrupted bool.

```go
type Recorder interface {
    Begin(context.Context, Start) error
    Prepare(context.Context, Target) error
    MayDispatch(context.Context, string, int) error
    Observe(context.Context, Receipt) error
    Finish(context.Context, Completion) error
}
type Repository interface {
    List(context.Context, Access, Query) (Page, error)
    Read(context.Context, Access, string) (Detail, error)
}
type Service interface {
    List(context.Context, Access, Query) (Page, error)
    Read(context.Context, Access, string) (Detail, error)
}
```

Recorder is injected into the trusted host. Every write matches the persisted
owner/key to the original trusted context; only Begin refreshes current key
permission. Revocation during execution must not discard final facts from the
already accepted binding. It grants no generation, request deduplication,
financial settlement or replay permission.

Access contains Session string and Administrator bool; requesting an admin
scope is not proof of a current role. Query contains State string, OwnerID/
TokenID uint and Limit/Offset int. Summary and AttemptView expose bounded
metadata. Owner projections omit actual model/group/protocol, channel/credential
IDs and quotation version. Detail contains record Summary plus Attempts. Page
contains Data, HasMore, Enabled and RetentionSeconds. Six nullable count strings
in UsageView distinguish null from "0" and preserve all nonnegative int64 values.
Host byte counts are exact decimal strings. No raw executor error is exposed.

## Persistent State And Fingerprints

Schema16 adds usage_invocations and usage_attempt_receipts. Existing steps and
rows are untouched; there is no inferred historical metadata or zero backfill.
The invocation stores trusted binding, bounded original model/operation/stream,
correlation, optional quotation digest, reserved MaxAttempts, timestamps/state,
final host facts and a canonical input/final fingerprint. Each child is keyed
by(invocation_id,number) and stores actual target, protocol, preparation/dispatch
state, host facts, six nullable int64 columns, usage source/completeness/profile
and canonical preparation/final fingerprints. String fields have explicit bounds.

Parent states are open/sealed/interrupted. Child states are prepared/may_dispatch/
observed/interrupted. Sealed/observed mean metadata was saved, not successful
generation, complete quantities, user charge or known Provider cost. No attempt
may be added to a sealed/interrupted parent or beyond the reserved bound.

Begin/Prepare duplicates succeed only for the same canonical binding/input.
Observe/Finish replays succeed only for the exact same canonical content;
conflicting terminal writes return ErrConflict and never overwrite. Nil counts
and zero have different fingerprints. Fingerprints exclude raw errors and
storage-generated timestamps. MayDispatch persists before the core call: a
crash afterward is possibly executed, even if no bytes actually reached upstream.
The marker never proves dispatch success. Final observations are the last
accepted cumulative snapshot, not a sum of SSE events or all-event journal.

Only the existing doRelay scope can bind each attempt's actual group/model. The
final request ChannelGroup must not be assigned to earlier retries. Metadata
uses accepted v3 facts before finishRequest's legacy estimation; unsupported old
decoder breakdowns remain unknown. The host records conservative dispatching
facts on panic and does not invent a returned result.

Finish seals only when all prepared attempts have observed terminal metadata.
An incomplete/panic scope becomes interrupted without erasing already observed
children. Exclusive single-instance startup converts orphan open/pending rows
to interrupted; it never resends, settles, refunds or infers missing quantities.

## Retention, Permissions And Lifetime

Retention is independent from logs, response idempotency, encrypted archives,
strict receipts and holds. Startup and Begin may clean at most1000 expired
terminal/interrupted parents per pass and their bounded children. Open live
records are never TTL-deleted; they stay counted and readable until sealed or
recognized as orphan at startup. Expired rows still consume capacity until
actually deleted. Expiry means metadata is no longer retained, not nonexecution.
No extra timer, worker, async writer or unbounded version/cache map is introduced.

All store operations honor finite contexts. Post-execution persistence uses a
two-second context detached from client cancellation while preserving the
original trusted binding. Closing check/active registration/Begin admission use
one gate. Close rejects new recorded work and joins admitted Begin, all attempt
writes, panic cleanup and final seal; it performs no I/O or wait under the gate.
Owners cancel/join execution before closing their Store, and main preserves its
existing shutdown ordering. Disabling capture never silently falls back for a
scope that was accepted as recorded.

GET /api/me/usage-records and GET /api/me/usage-records/:id are current owner
session queries. Corresponding /api/admin/usage-records routes request current
administrator scope. Both L3 and repository revalidate current sessions; SQL
owner predicates prevent cross-owner enumeration and reads return404 for absent
or forbidden IDs. Session checks reuse the existing owner-owned transaction
checker, including soft-deleted users, not an independent auth workflow.

List allows state,token_id,limit,offset and admin-only user_id, rejects repeated/
unknown/noncanonical parameters, defaults50,max100,offset<=10000. Read rejects
query parameters. Shared per-owner rate is120/minute, read deadline five seconds,
Cache-Control:no-store. API Keys and old management keys cannot replace sessions.
Errors are fixed400/401/403/404/429/503 without SQL/path/secret details. Disabling
new capture still permits authorized reads of retained prior metadata.

## Acceptance And Deferrals

Use real SQLite/current identity plus actual gateway/fake upstream JSON/SSE,
two distinct retry targets/groups, concurrent publication of price N+1, six
unknown/zero/large counts and restart. Inject Begin/Prepare/MayDispatch/Observe/
Finish failures, exact replay/lost acknowledgement/conflict and concurrent quota
claims. Process-kill proof covers accepted-before-dispatch, possible dispatch,
and returned usage before terminal write; restart stays interrupted and never
automatically executes. Verify cancellation/panic/Close joining, finite cleanup,
live-expiry safety and session revocation/soft deletion/cross-owner queries.

Keep all old tests. The new full final-head CI includes Linux race, full smoke
and schema16 container upgrade/backup/restore; old ledger balances, holds and
price format1 recovery must remain exact. The existing R05a-2 quotation remains
read-only; there is no aggregate charge, Provider tariff/invoice assertion,
paid background/WS or real OAuth/Provider/SDK acceptance in this batch.

Rollback disables recording after draining, retains metadata until its own TTL
and keeps its queries. Schema16 still requires a compatible binary or restored
backup, never an in-place old-binary downgrade. No new upstream source/dependency
is copied: reuse the existing TXAPI owner/session, receipt and immutable-fact
patterns. R05a/R05b and the overall reference-adoption goal remain separate.
