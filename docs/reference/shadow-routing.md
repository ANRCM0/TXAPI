# Shadow Routing Comparisons

This optional diagnostic compares a deterministic load/error recommendation
with the existing channel choice. Actual eligibility, highest priority,
channel weights, credential selection, affinity, retries and billing remain
the authority for every request. The recommendation never sends a request.

```yaml
server:
  route_diagnostics: true
  shadow_route_scoring: true
log_level: debug
```

Both startup flags are required. Scoring defaults false and cannot turn on
route diagnostics. Restarting with either flag false removes shadow sampling;
restart also intentionally discards all samples. There is no database, worker,
external call or persistent routing history for this comparison.

## Meaning And Limits

Only foreground HTTP host attempts are sampled. Active count spans the core
execution and its local attempt finalization, including output backpressure.
It is neither Provider capacity nor a latency measurement. Background, claimed
durable work and native WS do not contribute, so this is not total channel load.

An attempt adds at most one outcome: a clean complete HTTP 200, or a known
upstream HTTP 400..599 rejection without an excluded local fault. Errors in
preparation, admission, transport, protocol, output, cancellation, metadata or
billing finalization add no outcome. A 4xx may reflect client input; these
samples do not prove Provider responsibility. The existing core's fixed
error-body read failure is conservatively excluded, including when a Provider
returns that same message itself.

For each already eligible channel in an unpinned highest-priority pool of
2..8 channels, the diagnostic uses `(active+1)*(1+4*error_fraction)`. With
fewer than four live outcomes, the fraction is neutral 1/2; otherwise it is
`(errors+1)/(outcomes+2)`. Scores compare exact integer fractions, with channel
ID resolving a tie. Missing history receives neutral error treatment. Unknown
activity skips the entire comparison rather than supplying zero activity.

The process retains eight channel slots, 64 outcomes per slot and a ten-minute
window. Only inactive non-pool slots may be evicted. Busy storage, untracked
active work and old configuration attempts skip comparisons. A successful
configuration publication drops old outcomes and retains only one bounded
aggregate of old activity until all old attempts exit. A failed publication
keeps current samples; health refresh alone does not change the epoch.
Accounting or epoch saturation disables only this diagnostic until restart.

## Operational Consumer

The existing `/metrics` endpoint adds aggregate counters after the first
shadow observation:

| Metric | Meaning |
|---|---|
| `gateway_shadow_comparisons_total{reason}` | Compared or skipped observations using a fixed reason vocabulary |
| `gateway_shadow_eligible_total` | Enabled unpinned pools of 2..8, including skips for accounting state |
| `gateway_shadow_compared_total` | Comparisons with known activity for every candidate |
| `gateway_shadow_matched_total` | Recommendation equals the actual channel choice |
| `gateway_shadow_samples_total{outcome}` | Accepted clean HTTP 200 and explicit HTTP-error samples |

Reasons are `compared_equal`, `compared_different`, `disabled`, `pinned`,
`pool_size`, `slots_busy`, `stale_epoch`, `retired_active`, `untracked_active`,
`saturated` and defensive `unknown`. Disabled, pinned, pool-size and unknown
observations are excluded from the eligible count. No shadow metric has
channel, credential, user, Key, model, session, URL or request labels. Existing
metrics retain their values and semantics. The endpoint retains its existing
unauthenticated deployment access policy.

At debug log level, a comparison record includes numeric actual/recommended
channel IDs, epoch, pool size, fixed reason and up to eight candidates' active
counts, live outcomes, errors, neutral state and rational scores. The new record
is limited to one per second per gateway, with no queue. It contains no body,
URL, credential, owner, session, request ID or raw error. Existing unrelated
debug records keep their own behavior.

These diagnostics measure agreement and accounting coverage. They do not
establish better routing, throughput, latency, costs or real Provider behavior.
Any production scoring algorithm or default enablement requires separate
performance and traffic evidence. Scope and acceptance belong to the
[implementation decision](../../REFERENCE-R00-SHADOW-IMPLEMENTATION.md) and
[batch record](../../REFERENCE-ADOPTION-PROGRESS.md).
