# R00 Follow-on: Bounded Shadow Routing Comparison

Decision date: 2026-10-19. Concrete implementation boundary under the user's
existing authorization to continue the R00-R07 sequence with autonomous
implementation choices. This follows the accepted R06d source: final head
cee15e066eb112f2365e07b344d71fc9bf2c0d4e, PR #40, master/9a1264291a789e2e8d86c82614dd86f1ef12d20d,
with identical tree026daba87277e8baadd3d0279e72a0b9779cd05f. Complete push
39217668258 and PR39217752127 passed. Independent main39218975413 completed
full check/delivery success with no failed steps before source work. This decision is recorded
before implementation and supplies no acceptance evidence by itself.

## Six-Part Boundary

1. **Scope and owner.** One private L1 gateway change in `internal/gateway`;
   selection tests and config/assembly tests as required. A single gateway-owned
   in-memory sampler and scorer observe the current request's already eligible,
   non-continuation channels in the existing highest-priority pool. Preserve all
   current eligibility, priority, retry, credential-weight, extension picker,
   affinity, admission, execution, billing, and response behavior. Do not build
   a live scorer, switcher, persistent history, or generic scheduling framework.

2. **Facts and candidate rule.** Track observed active foreground HTTP host
   attempts, incrementing immediately before `core.Executor.ExecuteTo` and
   decrementing exactly once in the enclosing host-attempt defer after result
   postprocessing/finalization, including panic/early unwinding. That defer
   classifies the saved raw core result only if finalization has no excluded
   local fault; earlier pre-executor refusals acquire no token.
   This load includes client backpressure and host finalization; it is explicitly
   occupied host execution, not Provider RTT, service capacity, or latency.
   No elapsed-time or latency estimate participates in the score.

   Each normally returned attempt contributes at most one outcome. Copy the
   original core facts before gateway protocol remapping or billing finalization,
   then append an outcome only after the attempt finishes without an excluded
   protocol/local fault. With `Phase=ResponseReceived`, upstream HTTP status 200,
   `Termination=success`, and protocol complete, record a non-error sample.
   With that response phase and an explicit `UpstreamStatus` in 400..599,
   record an HTTP-error sample only if there is no cancellation, deadline,
   client-write/commit failure, local failure, or protocol/decode failure.
   Classify actual upstream HTTP rejection independently of later gateway
   status; never synthesize a sample from a generated 502/503 or cooldown.
   Other statuses, protocol/decode failures, panic/interruption, preparation,
   validation/admission, transport-without-response, and local metadata/billing
   faults contribute no outcome. They still release any valid active token.
   The fraction means observed HTTP rejection/error rate, not proof that the
   Provider caused an application failure; a 4xx can reflect client input.

   Keep eligibility as the existing authority. For a pool of 2-8 eligible
   channels with no pinned continuation target, compute a stable cost from the
   latest bounded observations: with fewer than 4 live outcomes use a neutral
   error fraction of exactly 1/2; otherwise use Laplace smoothing
   `(errors+1)/(n+2)`. The rational score is
   `(active+1)*(1+4*error_fraction)`; lower ranks first, ties by channel ID.
   Compare exact bounded integer cross-products, without floats or rounding.
   An unknown history receives the neutral error factor, never a zero-error
   bonus. Unknown/untracked active state is not zero: skip the whole comparison.
   Record the recommended channel and the channel chosen by the unchanged
   weighted/extension/affinity path. This is a deterministic comparison signal,
   not a claim that load/errors are representative or that the recommendation
   improves service. Pools outside 2-8 and pinned continuations are explicitly
   `not_compared`. Never invoke another picker, consume additional RNG draws,
   change extension/affinity calls, reorder actual selection, or alter weights.

3. **Bounds and lifecycle.** The sampler is allocated only when the existing
   `server.route_diagnostics` flag and the new opt-in
   `server.shadow_route_scoring` flag are both true; scoring defaults false and
   cannot enable diagnostics implicitly. Retain at most 8 channel states, 64
   outcomes per channel, and a 10-minute sample window. Use fixed arrays/rings and
   monotonic timestamps, pruning on observation/read; no goroutine, timer,
   external ID, user/session/key
   label, credential ID, model, URL, body, or raw error enters scorer state.
   Each slot contains channel ID, applied publication epoch, slot generation,
   active count, 64 outcome/time entries, and last-use time. Reuse/evict only
   inactive slots, choosing the least recently used inactive non-pool slot
   deterministically. Reserve existing pool slots during a comparison. If all
   slots are busy, a required pool slot is unavailable, or active accounting
   would exceed 65535 per slot, mark an explicit fixed skip reason; do not
   invent zero activity or an outcome. Any accounting saturation latches the
   shadow feature disabled until restart; configuration publication cannot
   clear this latch. Actual routing and publication continue unchanged.
   A bounded global untracked-active count covers starts that could not obtain
   a slot; their tiny epoch-tagged tokens decrement it on exit. While nonzero,
   skip comparisons rather than treating any possibly untracked activity as
   zero. Do not save their outcomes or create slots on completion. Saturation
   of this count also latches feature disablement, with no integer wrap.
   Samples cover only registered foreground host attempts; excluded native
   WS/background work is not represented as total channel utilization.

   Serialize sampler updates under its own mutex, never while holding the route
   registry lock, and never take the registry lock from a sampler operation.
   Attach a private monotonically increasing applied publication epoch to each
   channel in the immutable applied snapshot; credential HealthGeneration alone
   is not a configuration identity. Under the registry lock, finish the real
   successful Reload swap, stamp its snapshot epoch, and only then atomically
   store private `appliedEpoch`. Failure restores/preserves the previous channels,
   epoch, and samples. Health-only refresh retains the epoch. Epoch overflow
   latches shadow disablement until restart; it never fails actual publication
   or wraps/reuses an epoch.

   Every sampler operation loads atomic `appliedEpoch` under the sampler's own
   mutex and lazily advances its bounded state. Advancement carries the sum of
   the old eight slots' active counts and the old untracked-active count into
   one `retiredActive` aggregate before dropping old outcomes/slots and clearing
   current untracked activity. Existing retired activity carries forward too.
   No per-epoch map or retained slot array exists. Use checked uint64 aggregate
   arithmetic with a fixed 1048576 accounting cap; reaching/exceeding the cap
   latches shadow disablement until restart, never wraps or reports known zero.
   All sums take at most eight slot reads. While any retired or untracked
   activity exists, every comparison skips explicitly, even if its current
   candidate slots otherwise look idle. This deliberately trades observations
   for safe coverage during configuration churn; it never waits or affects
   production selection.

   Begin for a stale snapshot epoch increments retired activity and returns a
   retired token. Begin for the current epoch uses a slot or untracked activity.
   End first performs the same lazy advance: an old-epoch token decrements only
   retired activity; a current untracked token decrements only untracked; a
   current matching slot token decrements that slot. Each token is consumed
   once by its one private deferred owner. Old outcomes are discarded and old
   tokens never decrement a replacement slot or append current samples. A
   mixed/stale pool skips rather than moving the sampler epoch backwards.
   This covers old starts racing publication and completions that arrive
   before any new request first observes the publication. Scorer Close runs once,
   rejects new observations, drops the eight slots, and ignores late tokens.
   No worker join is added. Disable either startup flag to stop all sampling
   and comparison; restart intentionally clears samples.

4. **Consumer and surface.** Existing `/metrics` is the first consumer because
   it is already a supported operational endpoint and can show only bounded
   aggregate counters: eligible comparison count, compared count, sample count,
   recommendation==actual count, and not-compared reason. No channel-ID or
   arbitrary labels; fixed outcomes/reasons include disabled, pinned, pool_size,
   slots_busy, stale_epoch, retired_active, untracked_active, saturated,
   compared_equal, and compared_different.
   `/metrics` remains the existing unauthenticated endpoint: add only aggregate
   counts and no IDs or secrets, retaining its deployment-level access policy.
   No new routes/v1 declaration, query port, schema, or UI.
   With the existing root `log_level: debug`, emit a bounded comparison record
   containing numeric actual/recommended channel IDs, pool size, epoch, at most
   eight candidates' active counts, live outcome counts, neutral/known state,
   rational scores, and match/skip reason. Rate-limit these new records to one
   per second per gateway with no queue; counters retain total observations.
   Per-target numeric IDs belong only in operational debug logs. Exclude
   request/session/key/credential IDs, model, URL, body, and raw errors. The
   actual-main fixture must consume both metrics and captured debug output;
   this is an operational consumer, not a new session-administrator API.
   If aggregate metrics and opt-in debug
   records cannot answer the acceptance questions, stop before implementation
   and submit a separate consumer decision; do not add an unused v2 reader.

5. **Verification and acceptance.** First prove behavior with fixed clock and
   fixtures: exact eligibility/priority is unchanged; pinned continuation and
   pools of size 1 or >8 never compare; 0..3 samples use fraction 1/2; four
   samples activate the exact smoothed fraction; load/error ranking and ties
   are deterministic; expiry, saturation, successful epoch publication,
   failed publication, health-only refresh, channel removal, disabled flags,
   and repeated Close drop/ignore data
   as specified. Inject every excluded outcome class and prove it adds no
   outcome sample. Active acquisition/release must balance on every exit.
   Concurrent completions must count once and never exceed fixed storage bounds
   under sustained stress: eight busy slots plus a ninth channel must skip,
   inactive eviction must not corrupt an old completion token; old-epoch
   completions decrement retired activity exactly once, without updating
   current slots/outcomes. Hold an old same-channel attempt across multiple
   successful publications and prove no current zero-load comparison occurs
   until it exits; include stale-epoch Begin, old End as the first operation
   after publication, mixed pools, failed Publish, overflow, and feature-only
   disablement. Verify no registry/sampler lock nesting under publication and
   completion stress and no unbounded retained epochs.
   Prove actual weighted RNG/extension/affinity call sequences, upstream sends,
   output and financial state remain identical with the option on and off.

   Before enabling by default or proposing a live algorithm, capture a
   reproducible baseline against unchanged weighted selection using representative
   synthetic pool sizes and fixed sampling seeds: allocations/op, bytes/op, and
   p50/p95 selection latency with scoring disabled and enabled. Enabled overhead
   must be documented and bounded (target: no allocation per sample and <=10 us
   added p95 for an 8-channel pool on the recorded test host). Run a production
   selection-path benchmark before/after; add a regression threshold only if
   stable on CI. Shadow ranking accuracy is not service-benefit evidence. Any
   later claim about benefit requires a separately approved, controlled
   experiment and real traffic evidence; no traffic steering is in this batch.

   Existing `/metrics` family and route diagnostics operations must remain
   compatible. Frozen routes/v1 prior declarations must compare byte-for-byte;
   no new public signature is expected. Complete applicable local checks and
   exact-head automatic CI remain required for code acceptance.

6. **Rollback and exclusions.** Both flags false restores today's route path
   and emits no shadow state; disable and restart drops samples. Rollback never
   mutates credentials, cooldown, priority/weight, reservations, balances,
   retries, or in-flight requests. No database migration, Redis, external
   Provider call, credential/account evidence, scoring-based production pick,
   cost/price assertion, OAuth, WS/background sample ingestion, request-wide
   traffic history, frontend, routes/v2, or public contract freeze is included.
   This does not satisfy R04, R05b/full S5, R07, or deferred frontend work.

## Private Integration Sketch

These are concrete candidate private values and methods, not public contracts
or source changes. Their fields use only standard scalar/array/time types.
The sampler owns a pointer to the gateway's private atomic uint64 applied epoch
and its own mutex; each method performs lazy advancement before its work.

```go
type shadowCandidate struct { channelID uint; epoch uint64 }
type shadowToken struct { epoch uint64; slotGeneration uint64; slot int; kind uint8 }
type shadowOutcome uint8 // ignored, cleanHTTP200, explicitHTTPError only
type shadowComparison struct {
    reason string // fixed internal vocabulary
    recommended uint
    compared, matched bool
    // At most eight fixed candidate numeric snapshots; no dynamic fields.
}
func (s *shadowSampler) Begin(snapshotEpoch uint64, channelID uint, now time.Time) shadowToken
func (s *shadowSampler) End(token *shadowToken, outcome shadowOutcome, now time.Time)
func (s *shadowSampler) Compare(snapshotEpoch uint64, pool [8]shadowCandidate, count int, pinned bool, actual uint, now time.Time) shadowComparison
func (s *shadowSampler) Close()
```

`kind` includes tracked, untracked, retired, and consumed/disabled. A token is
private to one attempt's defer and never copied/reused. End consumes and zeros
the caller's token under the sampler mutex, making a repeated End on that same
token harmless without an unbounded token registry. Snapshot epoch is passed
by value; it does not replace checking current atomic appliedEpoch. Compare
never enumerates more than eight fixed candidates and never calls a picker.
Implementation must preserve the current successful Reload/rollback authority;
these signatures do not authorize a second publication or lifecycle owner.


## Reviewed Integration Constraints

Assembly adds only `gateway.Options.ShadowRouteScoring` and the startup
`server.shadow_route_scoring` field; neither package is a registered frozen
contract. All existing versioned public declarations remain byte-exact. No
new L1 public reader is needed: existing metrics and bounded debug records
are the concrete consumers.

The existing L0 result does not distinguish an upstream HTTP 502 rejection
from an error while reading that rejection's body with a dedicated typed
field. Preserve L0 exactly. Conservatively ignore its fixed host failure
message `failed reading upstream error`, as well as any disagreement between
actual upstream status and raw result status. This internal classifier is
protected by genuine-502 and broken-error-body regressions. An upstream
using the same message can only lose a sample; it cannot gain a healthy
sample. Do not log/store raw error text or infer Provider responsibility.

Fixed aggregate metrics are gateway_shadow_comparisons_total{reason},
gateway_shadow_eligible_total, gateway_shadow_compared_total,
gateway_shadow_matched_total and gateway_shadow_samples_total{outcome}.
Eligible means an enabled, unpinned pool of 2..8, including comparisons skipped
for accounting/epoch state. Reasons disabled, pinned, pool_size and unknown
are excluded from that count. The disabled ordinary path records nothing;
new families appear only after a shadow observation. Counter methods use
fixed preallocated atomic arrays, no dynamic labels or per-event allocation.


## Source Acceptance Checkpoint

Final local source passes complete build/vet/dependency/contract checks,
1707 tests/subtests and actual-main smoke on accepted master/9a12642.
The source-bound commands, fixed-ring allocation result and before/after
performance limitations are recorded in REFERENCE-ADOPTION-PROGRESS.md.
Final head6dc08bd9363cd6434c5f889ebdc709f747fd1060 passed complete automatic
push39223342711 and PR39223350698. PR #41 merged as
master/8ac00c95bfa74d72de83090c4c23f2f3443a5600 with the identical
ebf82faa0f306dd35e4aabbe571688ffcbef57ec tree. Independent main39224567863
also passed full check/delivery with no failed steps. The shadow slice is
accepted; no frozen API or live traffic scoring is added. Full R00-R07 scope
and its unresolved gates are retained in
[the status matrix](docs/reference/reference-adoption-status.md).
