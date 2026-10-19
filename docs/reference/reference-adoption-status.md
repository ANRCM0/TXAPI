# Reference Adoption Status

Verified on 2026-10-19 against master/bdcb307e18b029119f56f2cd4c8989318e60e51d (PR #42).
This is a status and evidence index for the complete
[R00-R07 design](../../REFERENCE-ADOPTION-DESIGN.md), not a replacement scope.
The overall objective is incomplete. Accepted sub-batches do not complete
broader R05/R06, S5 or A03-A06 programs.

## Current Integration Evidence

Remote master bdcb307 was checked online during repository/document cleanup.
Its complete Verify `39228308923` passed both check and delivery, including Linux
race, contract/build/vet/format checks, main smoke and container recovery.
OpenAPI is 0.11.0 with 111 operations; accepted main schema is 16.
This is baseline evidence, not CI for later uncommitted R04a or documentation.

The source evidence below retains PR #41's exact original head/tree and local
validation. R04a account authorization source is now in a separate uncommitted
worktree based on bdcb307, under its own concrete implementation decision.
It is not integrated or accepted, does not route accounts for inference, and
does not open real-account, charging or external Runtime gates.

PR #41 final head6dc08bd9363cd6434c5f889ebdc709f747fd1060 passed automatic
push39223342711 and PR39223350698. Its merged master/8ac00c9 has the identical
ebf82faa0f306dd35e4aabbe571688ffcbef57ec tree. Independent main39224567863
also passed complete check/delivery, including Linux race, contract checks,
main smoke and container delivery, with no failed steps. The source worktree
was clean and remote master was fetched and compared after completion.

Source and workflow records: [PR #41](https://github.com/ANRCM0/TXAPI/pull/41),
[final push](https://github.com/ANRCM0/TXAPI/actions/runs/39223342711),
[final PR](https://github.com/ANRCM0/TXAPI/actions/runs/39223350698) and
[merged main](https://github.com/ANRCM0/TXAPI/actions/runs/39224567863).

Local final source passed module/dependency checks, build, vet,1707 tests/
subtests in41 packages and full actual-main smoke. Seven expected process
helpers skipped;38 packages have no tests. Sixteen non-Markdown source files
match LF manifest202abef4f8f0e8a18157a8951a6c0a09e90f79956b56ee34531bf2778db20a4c.
Exact earlier heads, failed runs and correction evidence remain in the
[append-only batch record](../../REFERENCE-ADOPTION-PROGRESS.md).

## Complete Program Matrix

| Batch | Proven selected implementation | Unfinished work or entry condition |
|---|---|---|
| R00 | Routing observations and the default-off bounded shadow follow-on are accepted. Actual weighted/extension/affinity choices remain authoritative. | Live scoring/default enablement needs separate performance/traffic evidence; it is excluded from the accepted shadow slice. |
| R01 | Versioned execution facts, native optional token breakdown and real HTTP/SSE consumers are implemented and accepted. Compatible older contracts remain. | New operations/profiles require a concrete consumer and contract decision; candidate additions are not automatically frozen. |
| R02 | Native long-lived resource ownership, preparation/publication, retirement, drain, failed cleanup recovery and management/main wiring are implemented. | This does not implement a generic external Runtime or freeze every candidate port. |
| R03a/b | Bounded mechanical WS execution and a default-off disabled-billing native MVP are accepted; terminal handoff is corrected through PR #39. | Real Provider and broader client/SDK compatibility remain unproved. Paid WS, persistent reconnect and arbitrary multiplexing are outside the selected MVP. |
| R04 | Existing static API Key operation remains available. | R04a authorization/refresh/encrypted-account source has started in a separate uncommitted worktree. Integration/acceptance, inference use and full management remain incomplete; real-account acceptance is separate. |
| R05a | Selected HTTP/SSE cached/reasoning facts, immutable selling quotation history and durable foreground metadata/read APIs are accepted. | This is partial dimensional integration, not posted dimensional charging or all-transport capture. Further profiles require separate slices; frontend displays remain deferred. |
| R05b | Existing strict ledger, dual holds, immutable price/Provider snapshots and durable intent primitives are reusable foundations. | Full operation/failed-charge cost proof and costed native Responses/background/WS code remain incomplete. Disabled mode is still required for native background/WS. |
| R06 | Background owner/admin controls, recent routing reads, configuration status/apply, quotation/usage reads and personal financial reads exist. | Account management depends on R04. R06c and broader frontend/product work remain deferred; the whole program is partial. |
| R07 | Compile-time registrations remain the current extension mechanism. | External SDK/Runtime host/distribution code remains unimplemented. Entry requires at least two actual independently installable/upgradable consumers and insufficient compile-time registration. Examples/tests do not satisfy it. |

No additional missing behavior was identified inside the concrete selected
backend profiles/consumers. This does not turn the unfinished code above into
evidence-only work. The [design's dependency table](../../REFERENCE-ADOPTION-DESIGN.md)
continues to govern the next implementation batch.

WS/background detailed quantity/history capture is a possible future R05a
slice, expressly outside R05a-1/2/3. The
[R05a-1 decision](../../REFERENCE-R05A-IMPLEMENTATION.md) retains WS v2;
[R05a-3](../../REFERENCE-R05A3-IMPLEMENTATION.md) excludes native WS,
background and claimed capture. Adding every possible transport, payment,
quota or media profile is not a hidden acceptance condition for those slices.

## Design Acceptance Matrix

The following maps every risk row from design section8 to current proof and
its limit. A scoped pass is not proof for an unimplemented operation.

| Design requirement | Current evidence and limit |
|---|---|
| Owner/Key isolation and per-round authority | Existing real identity/HTTP/WS integration negatives run in full CI. New owner ledger/task/usage/routing reads retain session authority. OAuth account authorization is not implemented. |
| No attempt on selection/admission failure; pinned target cannot migrate | Selection/preflight and actual send-count regressions pass; shadow compares only the eligible priority pool and skips pinned continuations. |
| Single wire delivery, cumulative facts and no duplicate settlement | Codec/execution/ledger regressions and final SDK roundtrips cover selected HTTP/SSE/WS paths. New quantities/quotations do not become settlement authority. |
| Unknown sending survives partial I/O, timeout and process death | Existing durable dispatch/fencing and forced-kill recovery regressions pass. They do not authorize replay/refund of unknown native charged execution. |
| Publication lifetime, cleanup and last-owner close | Native resource generation and cleanup concurrency/race regressions pass; shadow retirement also preserves unknown old activity across publications. |
| Single refresh, stale CAS rejection and credential-state convergence | Not achieved for OAuth: its code and real account/protocol entry condition remain missing. Static credential health tests are not OAuth proof. |
| Dual-balance transaction, all-attempt cost and unknown preservation | Existing strict regressions pass for admitted dated Chat/embeddings profiles. Complete failed/rejected-request bounds, actual credential invoices and native operation cost proof remain missing. |
| Bounded capacity/backpressure and close | Existing HTTP/WS/worker/resource limits and new fixed shadow storage/overflow regressions pass. External-process Runtime isolation is not implemented. |
| Existing API and frontend compatibility | Complete CI preserves old contracts and Node/API checks; main smoke and ten controlled SDK cases pass. Broader Provider/client compatibility and deferred new frontend remain separate. |
| Dependency direction and public visibility | Full architecture/signature checks pass. This documentation batch changes no executable source or contract. |

## Fresh Controlled SDK Evidence

A newly built actual main from final head6dc08bd passed all ten preserved
official Python SDK scenarios in one run: Chat JSON/SSE, float embeddings,
Responses JSON/SSE, both function roundtrips, stored retrieve/local
continuation, background create/retrieve and controlled503 with no retry.
The locked local SDK3.35.1/httpx2 2.14.0 run made13 simulated Provider POSTs,
zero Provider GETs, no Provider errors and joined its Provider thread.
SDK/gateway retries were both0. This is one bounded successful sequence, not
an aggregate background-stability or full SDK claim.

BinarySHA25647ea8be4cc84dc0942dd6610e37dc0bcdddcca0da09a2dae99b469c2cba12304;
scriptSHA256fa5490e5f4b0824364a0ec59120cca407bd6a8ea82f0bc7e323cbaff88d9210a.
Source binding uses the explicit compile directory, matched source manifest/
tree, binary hash, frozen script and once-only execution record. Embedded Go
VCS data reports modified=true with no revision; no embedded clean-SHA claim
is made. A Windows LF/CRLF preflight fingerprint discrepancy was repaired
before execution; no SDK request occurred in that failed preflight.

Raw evidence is external TXAPI-references/validation-r00-shadow/sdk-6dc08bd.
Earlier failures and source-bound results remain preserved. No real Provider,
OAuth, native SDK WS, async/high-level streaming helper, strict charging,
background replay/cancel or universal SDK compatibility is proved.

## Next Entry Conditions

Keep the complete objective open. Select another implementation slice when
its documented prerequisite becomes true. R04a may finish the authorized source/
controlled-validation increment; real-account/Provider acceptance remains separate.
Other gates are independent full cost/operation evidence for R05b, the
user's frontend resumption for R06c, or two actual independent consumers for
R07. Do not fabricate Provider invoices, infer zero unknown cost, prebuild a
generic OAuth/Runtime platform, or relabel simulations to satisfy those gates.
No release, tag, image publication or production readiness is asserted here.
