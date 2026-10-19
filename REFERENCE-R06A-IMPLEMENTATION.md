# R06a: Background Task Console

Decision recorded before implementation, 2026-10-19. Base: master/2a6d162.
The user authorized autonomous follow-on design choices. R03b-2 is merged
and its functional/master CI passed; PR #31 documentation is now merged.
This batch is not R04 OAuth, R05 billing or R07 external runtime acceptance.

## 1. Six-Part Boundary

1. L1: new candidate taskcontrol/v1 value/query port, Store metadata projection
   and transaction-owned session authorization. No schema migration.
2. L3: taskconsole application consumes only this public port. L2: existing
   session-protected server/api adapter. Main injects the Store implementation.
3. Existing frozen background/v1, control/v1 and L0 signatures stay exact.
   New taskcontrol/v1 is a candidate, not frozen. No private L3 dependency.
4. Service methods are List(context.Context, Access, Query) (Page,error),
   Read(context.Context, Access,string) (Task,error), and
   Cancel(context.Context, Access,string,uint64) (Task,error). Access contains
   Session and Administrator (a requested scope, never authority). Query has
   Stage, OwnerID, TokenID, Limit and Offset. Repository has the same methods.
5. Test real SQLite/session roles, cross-owner isolation, demotion/logout,
   revoked/deleted creation Key, metadata-only projection, CAS/terminal cancel,
   actual queued/remote cancellation with existing worker, HTTP/OpenAPI and
   console pagination/stale responses/mobile. Then batch full checks and CI.
6. Shared main/server/OpenAPI/architecture/web are single-owner integration.
   No other feature or migration owner is modified in parallel.

## 2. Behavior And Tradeoffs

Use /api/me/background and /api/admin/background with list/detail GET.
Only /api/me/background/:id/cancel supports POST, requiring a decimal-string
revision in JSON. Admin scope is read-only; do not silently impersonate a
creation Key or add a privileged arbitrary-task cancel method.

Metadata lists public ID, owner/creation-Key IDs, model, internal stage,
revision, timestamps, cancellation flag, event counters and replay-pending.
Terminal means local generation state, not success, billing settlement or
complete event history. Expired/deleted content is visible only until the
existing metadata cleanup removes the row. No decrypt, raw errors, provider
IDs, input/output/event bytes, credential or claim tokens are read/returned.

Every read/mutation resolves the session hash, active account and requested
admin role inside the same DB transaction as resource filtering. Owner lists
cannot override owner scope. Session owner may cancel cleanup after creation
Key revocation; this grants no content access or new generation.

Reuse the existing background lock/cancel mutation, adding optional revision
CAS for the console. Repeat at the current revision is idempotent. An old
revision conflicts, including when an active worker changed it. Cancel records
intent, not confirmed remote cancellation; uncertain work stays uncertain.

Page size defaults to 50, maximum 100; offset <=10000; stable descending
created_at/public_id order with limit+1 has_more, no unbounded total scan.
The application reuses existing admission.Limiter at 120 console operations
per current session owner per minute, bounded state, no inference RPM charge.

Alternative: extend frozen background.Service or accept a caller user ID.
Rejected: it would either break old implementations or confuse session
management authority with creation-Key content authority. A separate small
metadata port matches the real consumer without duplicating task execution.

Rollback hides/removes new routes/pages after requests drain. Existing tasks,
state, encrypted content, migrations and call-Key API remain unchanged.
No retry/redispatch, billing mode expansion, account probing or release.
