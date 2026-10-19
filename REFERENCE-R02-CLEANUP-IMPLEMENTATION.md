# R02: Runtime Resource Cleanup Recovery

Decision recorded before source changes, 2026-10-19. Base is the tree-verified
R06b merge master/deb9e211c338288b288addb8d710617cab182590; its source tree
matches accepted head d12123d. R06b push 39191699174 and PR 39191725219 passed
complete check/delivery. Independent post-merge run 39192304586 is tracked
separately. The user's standing authorization covers choosing this corrective
batch before the next R06 page.

## Six-Part Boundary

1. L1 private gateway websocket resource ownership/publication and associated
   gateway regressions. Main assembly tests may verify the existing join retry
   and dependency lifetime. Documentation records R02 requirements and evidence.
2. Consume existing candidate NativeWebSocketService and frozen execution/v2
   leases. Existing public signatures, L0, L2 network mechanics, L3 business,
   schema, billing and HTTP contracts remain exact.
3. No new foundation port or general runtime framework. Repair the private
   recovery path: cleanup failures remain owned and occupy capacity; a subsequent
   serialized Publish/management Apply must retry eligible failed cleanup within
   a bounded context before preparing another candidate.
4. Cleanup may close only unreferenced discarded/retired generations, never the
   active current or pinned retired generations. Closing stays single-flight.
   A failure, cancellation or deadline preserves references and old current/
   applied state. A later Apply can complete cleanup and publish saved desired
   configuration; no duplicate save, resource leak or premature apply occurs.
5. Reproduce failed candidate and retired cleanup through the actual Publish or
   Configuration.Apply consumer, then verify retry recovery. Add necessary
   prepare cancellation/partial cleanup, concurrent duplicate lease release and
   short-deadline join/retry behavior. Main join failures must retain shared
   dependencies until resource and callback completion. Finish concentrated
   local checks and exact-head full CI/merge verification.
6. One agent owns gateway resource/publication sources and their tests; the
   integrating agent owns main assembly tests and records. No shared file writes
   or concurrent feature/interface/schema changes. R06c UI waits for this batch.

## Defect And Recovery Contract

Existing R03b production wiring owns real resources, preparation/publication,
retirement and drain; earlier R03a statements describe their historical point.
The missing path is runtime cleanup recovery: retryClose was only used by a
test, so a failed candidate blocked every future prepare and failed retired
resources could consume capacity until shutdown. Retaining those references was
correct; failing to offer the production retry was the defect.

The new private cleanup step uses a finite context and at most one new cleanup
attempt per failed generation per publication call. Pending/failed cleanup is an
explicit publication error, never a successful applied revision. Concurrent
publication remains serialized by the existing owner. No background retry loop
or unbounded new queue is introduced. Close completion must be observed before
reusing the candidate slot; the factory must not run while cleanup is uncertain.

Rollback stops new publication and drains owned resources before restoring the
prior binary. It must not drop failed references, force-close pinned resources,
clear financial state or replay provider requests. Full R02 acceptance and any
candidate-contract freeze require the mapped behavior evidence; this repair
does not prove real Provider/SDK compatibility, charging or all R00-R07 work.

## Requirements And Behavior Evidence

R03b-1 head 08c4c28 introduced the actual resource owner and controlled WS
consumer; R03b-2 head 81d2afe added production main/publication/session wiring.
Their exact CI/merge records remain in REFERENCE-ADOPTION-PROGRESS.md. The
following existing and added regressions are included in this corrective batch's
final full suite, rather than treating historical green runs as proof of new code.

| Requirement | Implementation / Regression |
|---|---|
| Real long-lived consumer; old streams remain pinned across publication | main responses_ws assembly; TestResponsesWSPublicationRetainsOldStreamAndRejectsOldSessionBoundary |
| Single bounded prepare; partial or canceled candidate is cleaned | websocketResources.prepare; TestWebSocketResourcesPrepareCancellationCleansPartialCandidate |
| Failed preparation leaves saved desired pending and old applied running | TestWebSocketManagementSaveAndApplyRetainCompleteOldSnapshot plus main publication lifecycle regression |
| Resource checks precede rollback-safe HTTP/native swap; retirement capacity is bounded | websocketResources.publish; TestWebSocketResourcesBoundRetirementAndFailBeforeApply |
| Existing same revision remains idempotent; retired revision cannot be reused | TestWebSocketResourcesPinRetiredResourcesAndDrain and retirement-capacity regression |
| Failed candidate cleanup can recover through the actual publisher | TestNativePublicationRecoversFailedCandidateCleanup |
| Retired cleanup retries retain current and never close pinned resources | TestNativePublicationRecoversFailedRetiredCleanupWithoutClosingPinned |
| Cleanup timeout retains the same worker/candidate; factory cannot run early | TestNativePublicationCleanupTimeoutRetainsCandidateAndDoesNotPrepare |
| Duplicate concurrent lease Release closes the retired resource once | TestWebSocketResourceConcurrentDuplicateReleaseClosesOnce |
| Short session Close timeout retains callback, capacity and lease ownership | TestNativeSessionShortCloseRetainsLeaseUntilCallbackJoins |
| Drain joins preparation and close workers; timeout does not invent completion | TestWebSocketResourcesDrainJoinsPreparingCandidate and live-lease drain regression |
| Main close error retains dependencies and joins the eventual retry | TestResponsesWSMainJoinRetriesCloseBeforeSharedDependencyTeardown, using the real assembly/runtime/store |

The new publisher regression first failed against the old code: expected two
Close calls after the retrying Publish, observed one. After the fix, the gateway
publication/resource/session/management group passed three consecutive runs.
The main retry test injects a first Close error and blocks the next one: the
join does not return and SQLite remains usable until cleanup is released. It
does not wait 130 seconds or claim that a real provider remained connected.

This maps the native resource lifecycle consumer and its corrective behavior;
it does not create or freeze a general external Runtime or all candidate WS
contracts. Exact final checks and integration state belong in the batch log.
