# Background Task Console

R06a is a metadata/control consumer of existing disabled-mode native tasks.
It does not enable background inference or expand billing. Implementation/CI
state is recorded in [the batch log](../../REFERENCE-ADOPTION-PROGRESS.md).

## Permissions And API

The console's Background tasks page uses the existing login session, never a
model-call Key. GET /api/me/background lists only the current user's tasks;
GET /api/me/background/{id} reads metadata. Administrators may use the same
GETs under /api/admin/background for cross-user diagnostics. The admin scope
is read-only; there is no administrator arbitrary-task cancellation endpoint.

List filters: stage, token_id, limit (default 50, max 100), offset (max 10000).
Administrator lists additionally accept user_id. Results have data/has_more,
ordered by created_at and public ID descending. This is bounded offset paging,
not a stable snapshot across concurrent creation/deletion. Refresh to reconcile.

POST /api/me/background/{id}/cancel requires JSON such as
`{"revision":"12"}`. Revisions are decimal strings, not browser floating-point
numbers. Stale versions return 409; refresh before another explicit action.
Repeated cancellation at the current version is idempotent. Terminal tasks
are not rewritten. A cancel request is not a refund or proof that remote work
stopped; uncertain work remains uncertain and is never resubmitted.

Session/user/role are rechecked inside the resource transaction. An owner may
request cleanup after the creation Key is revoked/deleted; this does not grant
content access, new generation or another user's authority. Session logout,
expiry and account disablement block future operations. Each current owner
shares a 120-operations/minute console bucket across sessions/scopes, separate
from inference RPM. DB metadata operations have a five-second context.

## Metadata And Privacy

Only public task ID, owner/creation-Key IDs, model, internal stage, revision,
timestamps, cancellation flag, event counters and replay-pending are returned.
Explicit SELECT columns exclude encrypted input/snapshots, event bytes,
credential data, provider IDs, claim tokens and raw errors; no decryption occurs.

Terminal is a local generation stage, not success, financial settlement or
complete event delivery. Content availability is TTL/deletion metadata, not
proof that stored bytes can still be decrypted. Metadata cleanup eligibility
is not a deletion promise: the existing owner retains uncertain/in-flight
records for safety. Deleted/expired content is not restored by these APIs.

The page has owner/admin scope, filters, bounded paging, metadata inspection
and explicit owner cancel. Leaving/switching the view aborts stale queries;
late responses cannot overwrite the current user/scope. IDs and values render
as text. Long IDs wrap on mobile; no content inspection UI is added.

## Integration And Rollback

L3 taskconsole consumes candidate taskcontrol/v1, existing session and limiter
ports. L2 API only parses/authenticates/presents. Store owns transaction-time
session checks and calls the same background cancellation mutation as the
existing call-Key API. Main is the composition root. No schema migration,
second worker, account refresh, ledger or recovery queue is introduced.

Remove/hide the new routes/page to roll back; existing tasks/content/keys and
call-Key cancellation remain intact. API is 0.7.0 / 98 operations, schema v15.
Original 93 operations/schemas and frozen declarations remain unchanged.
The new taskcontrol/v1 is a candidate, not a foundation freeze.

Checks: task store/API/worker tests, browser tasks.cjs, actual-main smoke.
Optional TXAPI_TASK_BROWSER=1 runs tasks-live.cjs within the disposable smoke
fixture when Playwright/Chromium are already installed. It never reads personal
configuration or contacts real providers. No Provider/SDK/release claim follows.
