# Recent HTTP Routing Diagnostics

R06b provides opt-in, read-only observations from the existing typed HTTP
generation entry. It does not change routing eligibility, priority, retry,
admission, authorization, protocol delivery or billing.

## Enable And Retention

Set server.route_diagnostics to true in the startup YAML and restart. It
defaults false. One gateway retains at most 512 completed scopes for 15 minutes,
with at most 16 selection observations and 11 attempt observations per record.
Request IDs, operation names and model names are bounded; metadata truncation is
explicit. No body, header, API Key, session token, provider URL or raw error is
copied. Only the requested model is available in owner scope.

Storage is process memory, with opportunistic expiry pruning. New completions
may evict older rows before TTL, and restart clears all observations. The API
does not enumerate active scopes. Missing data does not prove that no request
ran. Ordinary request logs, this buffer and financial/recovery state are
different sources; diagnostics cannot settle, retry, cancel or recover work.

## Authority And API

Four GET operations use existing login sessions:

- /api/me/routing lists the current owner's retained observations.
- /api/me/routing/{id} reads an observation within that owner.
- /api/admin/routing lists across owners after current administrator checks.
- /api/admin/routing/{id} reads one retained administrator projection.

The application calls the existing control/v1 SessionUser authority on every
query, including direct service calls. Logout, expiry, disabled/deleted accounts
and demotion block subsequent queries. These immutable in-memory observations
are authorized at that session check; they do not introduce a database mutation
or cross-store transaction. A deleted creation Key does not erase its owner's
already-retained metadata or permit new generation.

Owner scope always removes channel/credential IDs and actual mapped models,
including when an administrator selects My requests. Administrator scope may
include those diagnostic values, never credentials or content. Each session
owner shares a 120-query/minute routing-console bucket across sessions/scopes;
this is separate from inference limits and the background task-console bucket.

List accepts request_id, token_id, limit (default50/max100) and offset
(0..10000); administrator scope also accepts user_id. Request ID filters are
exact safe ASCII strings of 1..64 characters. Unknown/duplicate query fields are
rejected. Detail permits no query fields. Errors are 400/401/403/404/429/503;
429 includes Retry-After: 60. Responses have Cache-Control: no-store.

Opaque rt_ IDs identify one invocation. Client Request IDs are untrusted
correlation metadata: matching IDs return separate records and never share
state, cancellation, budget or authority. Offset paging reflects current
retained rows, not a stable snapshot; refresh to reconcile concurrent arrivals.

## Meaning And Coverage

Selection facts describe candidate/credential eligibility, the priority pool,
fallback and actual picker. A selected target may still be rejected before
network execution. Attempt facts preserve the existing dispatch observation,
local status, HTTP commitment and protocol completion. They do not prove an
upstream invoice, client application consumption or financial settlement.
An interrupted execution scope reports unknown aggregate dispatch/termination;
previously completed attempt facts remain observations, not replay permission.

The first consumer is the HTTP executor for Chat, embeddings and foreground
Responses. Authentication or adapter failures before that entry, non-HTTP
typed consumers, native background controls and native WebSocket turns are
not recorded. The collector belongs to the private execution scope; it is not
placed in a reusable context or indexed by an external Request ID.

The Routing diagnostics console supports owner/admin scope, Request ID and
owner/Key filters, bounded paging and read-only detail. Scope changes/navigation
abort stale queries. A failed page fetch is an observation-query failure, not
a task/inference outcome. Values render as text and long IDs wrap on mobile.

## Integration And Rollback

L1 gateway owns the optional bounded buffer and routes/v1 Reader. L3
routeconsole consumes that candidate port, frozen session/rate capabilities and
no implementation packages. L2 server/api parses/presents; main composes the
instances. Existing public declarations remain exact; routes/v1 is a candidate,
not a new foundation freeze. No schema migration or persistent record is added.
OpenAPI appends four operations as 0.8.0 / 102 operations.

Disable the startup flag and hide/remove the new routes/page after draining to
roll back. No bill/task/provider execution is deleted, refunded or replayed.
Exact checks, commits and integration state belong in
[the batch log](../../REFERENCE-ADOPTION-PROGRESS.md) and
[the six-part decision](../../REFERENCE-R06B-IMPLEMENTATION.md).
