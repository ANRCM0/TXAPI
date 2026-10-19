# Archive Preparation Cancellation At Runner Shutdown

Decision before correction, 2026-10-19. Base master/5998e43a7e80a32aa755bd81980bf2b7889c9130
matches accepted R05a-1 head a440a4d. Both complete push/PR runs passed;
independent merged-main run39197079265 failed the existing race regression
TestArchiveHandoffDoesNotHoldShutdownLockAndCloseJoinsStorage:
"canceled storage operation committed: 1". Delivery succeeded. This failure
is not dismissed as a passing main or hidden by retrying the workflow.

## Six-Part Boundary

1. L1 internal/generation Runner owns preparation cancellation and joining.
   Fix only private runner state and targeted archive/runner regressions.
2. Existing frozen execution/state/lifecycle contracts, ArchiveStore and the
   current trusted binding resolver remain exact. No public signature changes.
3. No L0, schema, financial logic, request execution, network transport or
   publication change. The owner already promises to cancel in-flight handoffs
   and join them before shared gateway/store dependencies can close.
4. Root cancellation currently reaches storage preparations through
   context.AfterFunc(r.ctx,cancel). That callback is asynchronous. Close with
   an already-canceled waiting context can return before it cancels the storage
   context; a released storage fixture can then commit a supposedly canceled
   pre-handoff record. Replace this scheduling race with synchronously owned
   cancellation registered under the same shutdown mutex. Do not perform
   storage I/O or wait for storage while holding that mutex.
5. Reproduce canceled storage commit/cancellation ordering, retain the old
   shutdown lock/join/zero-row assertions, and add deterministic ownership
   observations before releasing blocked storage. Cover cancellation prior to
   registration, concurrent preparations, external parent shutdown, cleanup on
   failure/panic and retry Close. Directed repeated tests first, concentrated
   full build/vet/test/contracts/smoke, final-head automatic Linux race/delivery,
   merge-tree and independent main results remain mandatory.
6. Protocol agent owns generation/runner.go and archive_test.go only;
   integration owner reviews, records and delivers. Quotation backend is safely
   checkpointed locally on feat/reference-r05a-quotation/2c42b11 with no push/PR;
   it resumes after this independent correction is integrated.

Each accepted preparation must either register cancellation before shutdown
can return, or observe an already-closed runner and avoid storage. Owner state
is bounded by admitted pending submissions and is released on every exit.
Storage still receives the original observer values/deadline and may only
finish under its proper context. In-flight transactions that already committed
before cancellation remain observations, never proof of upstream execution;
this correction concerns a call still blocked before entering the real store.
Close's canceled/timeout wait does not claim joining, and the owner still keeps
dependencies alive until a later successful join. No timeouts are increased
and no existing assertions or regression tests are removed.

Rollback may restore the prior implementation but must report the unresolved
shutdown race. No archived body, unknown execution or balance is rewritten.
This is an existing lifetime-contract correction, not a new foundation freeze
or completion of R05a-2/R05b/the full reference-adoption goal.
