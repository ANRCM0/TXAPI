# R06b: Recent HTTP Routing Diagnostics

Decision prepared before source changes, 2026-10-19. Follow the user's
standing authorization to compare choices and implement the best bounded
increment. Base: master/3d55065d9fcf63265aa0b7576c22c118d9964371,
tree-identical to accepted R06a head 8edb499. R06a push/PR full CI passed;
its independent post-merge run 39188756506 also passed check/delivery.
This is the next R06 page, not OAuth, billing expansion or an external Runtime.

## 1. Six-Part Boundary

1. L1 gateway owns optional, bounded ephemeral route observation and its read
   adapter; config/main opt in at startup. New candidate routes/v1 exposes only
   owned query values and capability interfaces. L0 stays exact.
2. L3 routeconsole consumes routes/v1 plus frozen control/v1 SessionUser and
   admission/v1 Limiter. L2 server/api parses/presents four login-session routes.
   Main composes these capabilities; L3 never imports gateway/store/core.
3. Frozen execution, background, control and other declarations stay byte-exact.
   The new routes/v1 port is a candidate and is registered by exact layer/owner.
4. New operations: GET /api/me/routing and /api/me/routing/{id}; identical read
   methods under /api/admin/routing. OpenAPI appends these without rewriting old
   operations/schemas. No schema migration or mutation endpoint.
5. Validate duplicate Request IDs across owners/concurrent generations, bounded
   memory/TTL/eviction and defensive copies; selected-versus-dispatched facts;
   preflight/admission/budget failures and retries; current session logout,
   role demotion, disable/soft deletion; target-ID redaction; HTTP contracts,
   desktop/mobile stale response behavior, actual-main loopback smoke. Finish
   concentrated full local checks, then exact-head automatic CI and merge.
6. Shared gateway/main/config/API/OpenAPI/architecture/web changes are integrated
   by this one batch. No other feature module or migration owner is in flight.

## 2. Query Contract And Authority

Candidate routes/v1 values:
- Access: Session string; Administrator bool (requested scope, not proof).
- Query: RequestID string; OwnerID/TokenID uint; Limit/Offset int.
- Scope: UserID uint; Administrator bool. Constructed by the application only
  after current SessionUser checks. Query ports are injected capabilities, not
  public HTTP authentication mechanisms.
- Selection: fixed local outcome/picker; candidate, eligible, priority-pool and
  credential counts; priority fallback; duration; optional target channel/Key IDs.
- Attempt: ordinal; status/HTTP commit/phase/termination facts; optional target
  IDs and bounded actual model (administrator scope only).
- Trace: opaque per-invocation ID; bounded RequestID/model/operation metadata;
  owner/Key IDs; start/end times; final local status/HTTP status, completion,
  dispatch/termination/commit/protocol observations; bounded selections/attempts;
  truncation flag. No bodies, headers, session/key strings, URLs or raw errors.
- Page: enabled/retention/capacity metadata; data/has_more. No cross-owner totals.
- Reader.List(ctx, Scope, Query) (Page,error), Read(ctx, Scope,id) (Trace,error).
- Service.List(ctx, Access, Query) (Page,error), Read(ctx, Access,id) (Trace,error).
- Errors: unauthorized, forbidden, invalid, not found, rate limited, unavailable.

The L3 service rechecks the session/user/current role for every query using the
existing frozen control authority, before accessing in-memory observations.
A read is authorized at that check; records are immutable snapshots, no durable
resource mutation or cross-database transaction is involved. Owner filters are
fixed to the session owner; users cannot request another owner. Administrators
may read across owners and filter user_id. Target channel/credential IDs are
removed from all owner-scope projections, including administrators' own scope.
Current Key revocation does not erase an owner's historical metadata; no content
or new generation authority follows from reading it. Shared per-owner console
rate is bounded separately from inference limits; session tokens are not stored.

Query input permits one exact request_id (1..64 safe ASCII characters), token_id,
admin-only user_id, limit default50/max100 and offset0..10000. Repeated or unknown
keys are rejected. Detail IDs are server-generated opaque trace IDs. A caller
Request ID is untrusted correlation only: equal IDs produce separate records,
never a shared cancellation or authorization scope. No arbitrary model/error
text is evaluated or rendered as HTML.

The exact new interfaces are:

```go
type Reader interface {
    List(context.Context, Scope, Query) (Page, error)
    Read(context.Context, Scope, string) (Trace, error)
}
type Service interface {
    List(context.Context, Access, Query) (Page, error)
    Read(context.Context, Access, string) (Trace, error)
}
```

Trace values use string IDs/request IDs/operation/model/reason/phase/termination,
uint owner/Key IDs, int status/HTTP status, int64 Unix-millisecond timestamps,
bool committed/protocol_complete/interrupted/truncated, and owned Selection and
Attempt slices. Selection and Attempt target IDs are uint with omitempty and
are zeroed for owner scope; actual_model is also omitted for owners. Page has
Data []Trace, HasMore/Enabled bool, Capacity/RetentionSeconds int. No capability
accepts a database, execution implementation or mutable generic extension map.

## 3. Observation And Resource Ownership

Startup-only server.route_diagnostics defaults false. When enabled, one gateway
owns at most 512 completed traces retained for 15 minutes. Each trace has at most
16 selections and 11 attempts, RequestID<=64, operation<=64, model<=128 bytes;
other strings come from fixed local enums. Oversize metadata is omitted or
truncated and marked; no large input backing array is retained. New completions
can evict older traces before TTL. Reads prune expired entries and return copied
slices/values. No worker, database, external I/O or new financial authority.

The typed generation entry attaches a fresh private collector after verifying
its Principal. Existing selection emits its facts into that collector without
changing the chooser. Completion snapshots copy the existing attempt/result
facts after normal cleanup; aborted scopes are marked aborted/unknown rather
than manufacturing an unsent or successful result. Observation is independent
of the ordinary droppable request log, and is never recovery or ledger truth.
No public payload can inject a collector, target or result.

Initial coverage is executions through gateway's existing typed HTTP generation
path: Chat, embeddings and foreground Responses, including their preflight
failures once a trusted generation reaches that entry. Authentication failures,
requests rejected before the entry, private native background controls and
native WS turns are not captured by this slice. It is not durable audit/history,
complete traffic accounting, cost evidence or proof of client consumption.
Restart clears it; active requests are not listed until their scope finishes.

## 4. Product Behavior And Compatibility

The Routing diagnostics page supports owner/admin scope, bounded paging, an
exact Request ID filter and metadata inspection. It separates selection from
attempts, protocol completion from delivery and local status from HTTP commit.
The page communicates disabled recording, ephemeral retention and missing or
evicted records; missing data does not prove a request never ran. Leaving or
switching scope discards late queries. Existing visual/UI patterns stay intact.

No changes to routing priorities/weights/health, retry eligibility, requests,
Responses history, task worker, billing allowlist, SQL/migrations or core I/O.
Alternative: persist verbose per-request logs or expose existing Debug output.
Rejected for this slice: Debug output is not an owner-authorized query model;
durable verbose observation adds retention/migration cost and risks confusing
it with audit/financial state. The bounded opt-in projection proves the real
consumer before any separate durable diagnostics requirement.

Rollback disables the startup flag and hides new routes/page after requests
drain. No task, bill or provider execution is deleted or replayed. Real Provider,
SDK, OAuth, charging and external Runtime gates remain independent.
