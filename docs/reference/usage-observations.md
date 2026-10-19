# Native Token Breakdown Observations

R05a-1 adds candidate `execution/v3`, a native Responses JSON/SSE decoder and
the independent L3 `examples/facts.UsageService`. Existing frozen declarations,
HTTP operations, database schema v15 and financial permissions are unchanged.
The [decision](../../REFERENCE-R05A-IMPLEMENTATION.md) defines exact signatures;
the [batch log](../../REFERENCE-ADOPTION-PROGRESS.md) records actual acceptance.

## Quantity Contract

An observation is a cumulative snapshot for one attempt. It contains optional
input/output/total Token counts plus optional cached-input, cache-write-input
and reasoning-output counts. Nil means unknown; a pointer to zero means an
explicit provider zero. Missing fields are never inferred from older snapshots.
The host retains private high-water marks to reject decreases across missing
observations, without filling in unknown fields in the returned snapshot.

The native profile is `responses_native_tokens_v2`. Native fields are
`input_tokens`, `output_tokens`, `total_tokens`,
`input_tokens_details.cached_tokens`, `input_tokens_details.cache_write_tokens`
and `output_tokens_details.reasoning_tokens`. Unknown aliases do not contribute
new quantities. Known values must be nonnegative int64 integers. Explicit
parents bound their children; cached plus cache-write is bounded by input,
reasoning by output, and an explicit total must equal input plus output when
both are known. Checked arithmetic rejects overflow.
Known children supply lower bounds for unknown parents, so an explicit total
smaller than those lower bounds is rejected without inferring parent counts.

Completeness is Unknown when all quantities are unknown, Partial when any are
known but a base count is missing, and Complete when all three base counts are
known and consistent. Complete does not assert that every optional detail is
available. Known observations carry provider-reported provenance and the profile.
They do not prove provider invoices, client consumption or charging permission.

## Execution And Compatibility

Composition injects `gateway.NewUsageFactsExecutor(g)` into a consumer accepting
`execution/v3.Executor`. The example refreshes the current trusted identity for
every invocation and uses the same gateway execution exactly once. Production
L3 source imports only public identity/execution contracts; only its test
composition root constructs the real store, gateway and controlled upstream.

The gateway selects one decoder capability: v3, then v2, then v1. A v3 error
terminates that protocol attempt; it never falls back and delivers the same
representation again. Each envelope contains one wire representation, an
explicit old v2 base projection and optional enriched observation. The host
checks equality of the base counts, validates provenance and consistency,
and keeps dispatch/protocol/delivery facts under host ownership. Results own
their slices and count pointers; separate attempts cannot share mutable state.

Base fields retain the existing Go JSON compatibility matching and machine-int
range; full int64 base-count coverage is verified on amd64. New nested child
lookups use exact names. The enclosing JSON containers retain the existing
case-insensitive compatibility matching. No unrelated alias fallback is added.

Old v2 consumers keep the native `responses_native_tokens_v1` base projection.
A detail-only v3 observation projects to Unknown base usage. Only a complete
terminal base snapshot can feed the existing accounting path. Native WS stays
on its existing v2 path; this batch does not add a WS detail profile. Existing
direct decoder v1/v2 methods retain their old behavior. New v3 validation may
reject malformed details that those old direct methods did not interpret.

No breakdown persistence, prices, ledger mutations, query routes or frontend
are added. Responses/background/WS strict billing remains closed. Later pricing
work must define inclusion and rounding, preserve old records by version and
prove all permitted cost dimensions before opening charging.

## Extension, Validation And Rollback

New codecs declare profiles and explicit v2 projections through the candidate
Decoder interface; L1 does not interpret provider JSON or branch on profile
names. Each stream owns its bounded parser/session. New profiles require their
own containment and cumulative-snapshot regressions. Never add arbitrary raw
metadata or retain request/response bodies in observation values.

Run `GOARCH=amd64 go test -count=1 ./internal/foundation/contracts/execution/v3
./internal/protocol/responses ./internal/gateway ./internal/examples/facts` in a
shell with the repository's normal Go environment; PowerShell sets
`$env:GOARCH='amd64'` separately. The batch additionally runs complete build,
vet, architecture/signature/HTTP contract tests, full ordinary local tests and
main-process smoke, then the existing full Linux race/delivery CI on its final
head. Controlled HTTP/SSE tests do not replace real Provider/SDK acceptance.

Rollback drains requests, selects the existing v2 decoder/consumer and removes
the candidate facade. No historical financial record is rewritten and no call
is replayed. Third-party adaptations and the retained upstream license are
listed in [source notices](../../THIRD_PARTY_NOTICES.md).
