# Durable Usage Metadata

R05a-3 adds an opt-in foreground invocation/attempt metadata record. Quantities,
selling quotations, committed customer charges, Provider spend and budget holds
remain separate facts. This capability starts no second executor, billing ledger
or asynchronous log writer. The new usagehistory/v1 contract is candidate.

## Enable And Bound

```yaml
billing:
  mode: disabled
usage_history:
  enabled: true
  instance_records: 4096
  owner_records: 256
  retention_seconds: 604800
```

The feature defaults off. Enabled capture is accepted only with disabled billing;
legacy/strict combinations are rejected at startup and gateway construction.
Limits are finite: 1..65536 instance parents, 1..instance owner parents and
1..2592000 retention seconds. Zero in startup configuration selects the finite
default. Each parent reserves up to retry_times+1 child slots, maximum 11.
These are logical metadata row bounds, not a disk/WAL size guarantee.

Supported capture uses the existing foreground gateway coordinator: Chat,
embeddings and native foreground Responses JSON/SSE, plus explicit public/facts
foreground consumers. Native WS, native background submit/poll and durable claimed
execution are excluded by internal source. Responses idempotent replays do not
execute again or create another generation record. Feature-off paths retain
their previous behavior. A page's enabled flag describes new capture, not the
completeness of every request through the server.

## Acceptance And Failure Meaning

Recording begins only after identity, parsing, model permission and request/rate
admission pass. Earlier rejections are not promised a record. A server-generated
usage_ plus 32 lowercase hex ID identifies one accepted invocation. X-Request-ID
is only an optional correlation string; repeating it does not deduplicate Chat
or authorize a replay. The response may carry X-TXAPI-Usage-ID after Begin and
before commit. The final record state is available through the query API.

Before each selected target executes, Prepare saves the actual model, group,
channel/credential binding and protocol. MayDispatch then commits a conservative
marker before calling the existing executor. A crash after that marker is
possibly executed, even if upstream received no bytes. Begin/Prepare/MayDispatch
failure prevents the affected unsent attempt and returns fixed
usage_recording_unavailable. Failure on a later retry does not erase an earlier
executed attempt or claim that the whole invocation had zero upstream calls.

Observe saves the actual host facts and last accepted cumulative v3 observation;
it does not add SSE snapshots together. Finish seals the invocation only after
all prepared children are observed. After execution, persistence failure preserves
the real Outcome and already delivered JSON/SSE, stops further upstream retries
and leaves recording uncertain/incomplete. It never reruns generation to repair
a record. A lost database acknowledgement may accompany an already committed
exact write; only replaying the same canonical metadata write can confirm it.

The quotation content digest is captured before execution when available. A
missing digest stays absent, and publication of N+1 cannot replace the retained N.
This is selling metadata, not the strict reservation price or a committed fee.
No current price is retroactively presented as the historical charged amount.

## State And Quantities

| Record | States | Meaning |
|---|---|---|
| Invocation | open, sealed, interrupted | Accepted, metadata sealed, or interrupted/incomplete |
| Attempt | prepared, may_dispatch, observed, interrupted | Target saved, possibly dispatching, terminal metadata saved, or interrupted |

Sealed/observed do not imply successful generation, complete usage, known Provider
spend, invoice correctness or financial settlement. Protocol completion, delivery
commit, HTTP/upstream status, termination and bytes accepted by the output port
remain distinct. Accepted output bytes do not prove client consumption.

Each attempt stores six independent nullable int64 quantities: input, output,
total, cached input, cache-write input and reasoning output. Null is unknown;
"0" in JSON is known zero. Profiles retain inclusion rules and provenance. New
records use the last accepted snapshot, not inferred visible-text counts or the
legacy finishRequest estimate. Old decoders without v3 observations remain
unknown even when older billing/log projections contain estimated/base counts.

All base and breakdown validation still applies: nonnegative integers, int64
overflow, cumulative monotonicity, containment and input/output lower bounds.
This metadata contains no prompt, response, tool argument, raw error, arbitrary
extension object, URL, header or credential secret.

Repeated Begin/Prepare is idempotent only for the exact original binding/input.
Repeated Observe/Finish succeeds only with the same canonical fingerprint.
Nil/zero and all pricing/target/host facts that were actually recorded remain
distinct. Conflicting terminal writes never overwrite. An exact metadata replay
does not authorize another upstream execution or create request idempotency.

## Session Query API

| Method | Path | Scope |
|---|---|---|
| GET | /api/me/usage-records | Current owner's retained invocation summaries |
| GET | /api/me/usage-records/:id | Current owner's detail and attempts |
| GET | /api/admin/usage-records | Current administrator diagnostic summaries |
| GET | /api/admin/usage-records/:id | Current administrator target diagnostics |

The L3 use case and repository both check current sessions/roles. SQL constrains
owner reads; absent and other-owner IDs both return 404. API Keys and the old
management key cannot replace login sessions. Owner views omit actual model,
group, protocol, channel/credential IDs and the selling quotation digest.
Administrator scope is explicit and independently authorized.

List accepts state, token_id, limit and offset; user_id is administrator-only.
State is open/sealed/interrupted, default limit50, maximum100, offset0..10000.
Repeated, empty, unknown, negative and noncanonical numeric parameters are rejected.
Detail rejects query parameters. Results are recent bounded metadata, not a
complete accounting export. Shared owner query rate is120/minute with a finite
five-second read context and no-store responses. Errors are fixed400/401/403/
404/429/503 and never expose SQL or paths.

All six JSON counts are null or canonical nonnegative int64 decimal strings.
Host bytes_written is also an exact decimal string. For example:

```json
{
  "input_tokens": "9007199254740993",
  "output_tokens": "0",
  "total_tokens": "9007199254740993",
  "cached_input_tokens": null,
  "cache_write_input_tokens": null,
  "reasoning_output_tokens": null,
  "completeness": "complete",
  "source": "provider_reported",
  "profile": "responses_native_tokens_v2"
}
```

Complete base counts do not imply known breakdowns. A later selling calculation
can therefore remain unknown; do not substitute zero for missing cache fields.

## Restart, Retention And Rollback

Schema16 appends usage_invocations and usage_attempt_receipts. Existing migration
steps, financial rows, price documents, archives and response receipts are not
reinterpreted or backfilled. Exclusive single-instance startup marks orphan open
and pending records interrupted, retaining already observed children. It does
not generate, settle, refund or infer missing usage. Utility CLI operations do
not run the server's orphan-recovery step.

Startup and Begin clean at most1000 expired terminal/interrupted parents and
their bounded children per pass. Live open records are never TTL-deleted and
remain counted/readable; restart must first identify orphans. Expired rows still
occupy capacity until actually deleted. Metadata expiry never deletes a billing
hold, changes response idempotency or proves that no execution happened.

Writes are synchronous and finite. Post-execution writes detach client
cancellation while retaining the trusted owner/key binding. Gateway shutdown
rejects new recorded work, joins every admitted recording including failures,
panic and final seal, then permits the owner to close Store. No new writer queue,
worker, timer or cache lifecycle is introduced.

Disable capture after draining to stop new writes while retaining authorized
queries until expiry. Schema16 requires a compatible binary or restored backup;
disabling the feature is not an in-place database downgrade. This batch grants
no paid background/WS capability, Provider invoice/cost acceptance or real OAuth/
SDK compatibility. Frontend work remains deferred.
