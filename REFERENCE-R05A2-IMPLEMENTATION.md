# R05a-2: Versioned Dimensional Selling Quotes

Decision recorded before implementation, 2026-10-19. Base is accepted
master/5998e43a7e80a32aa755bd81980bf2b7889c9130, tree-identical to R05a-1
head a440a4d. Push 39196504025 and PR 39196509658 passed full check/delivery;
independent post-merge 39197079265 is tracked separately. Backend remains first,
frontend is deferred and OAuth real-account acceptance stays closed.

## Six-Part Boundary

1. L1 billing owns immutable dimensional selling documents, publication and
   checked quote arithmetic. Store reuses append-only BillingPriceVersion
   history with a bounded version reader. Config/main own opt-in composition.
2. New candidate quotation/v1 Reader/Service and values consume execution/v3
   observations. Frozen identity/control/admission support independent L3
   quoteconsole administration. Existing registered declarations remain exact.
3. L0, balances, holds, old price algorithms, settlement/recovery, schema v15,
   upstream execution and financial permission do not change. The new quote
   document never replaces PriceTable or a BillingReservation.PriceVersion.
4. Exact candidate signatures and two admin-session HTTP routes are below.
   All existing operations/schemas and consumers stay exact. No frontend.
5. Validate unknown/zero, inclusion without double counting, one upward rounding,
   large intermediates/overflow, version pinning/publication failure/restart,
   legacy format1 exact recovery, current admin authorization and rate limits,
   precise string HTTP quantities and real JSON/SSE generation-to-quote use.
   Finish concentrated complete local checks, final-head full CI and merge.
6. Billing agent owns billing/quotation* files and tests only. Integration owner
   owns contracts, registry/snapshots, history reader/config/main, L3 consumer,
   server/API/OpenAPI and smoke. No other migration/publication owner is active.

## Data And API Contract

Candidate quotation/v1 defines Version="1", fixed errors Invalid, NotFound,
Unavailable, Overflow, Unauthorized, Forbidden and RateLimited; names are
ErrInvalid etc. `Access` contains Session string. `Input` contains PriceVersion,
Model and Group strings and execution/v3.UsageObservation Usage. `Current` is
a value containing Enabled bool and Version/Currency/PriceProfile/UsageProfile/
UnitScale strings. `Quote` contains PriceVersion/Model/Group/Currency/
PriceProfile/UsageProfile/UnitScale/AmountUnits/Reason strings and Known bool.

```go
type Reader interface {
    Current(context.Context) (Current, error)
    Calculate(context.Context, Input) (Quote, error)
}
type Service interface {
    Current(context.Context, Access) (Current, error)
    Calculate(context.Context, Access, Input) (Quote, error)
}
```

All JSON DTOs use snake_case. Current.Version and Input.PriceVersion are
lowercase SHA-256 hex of the exact canonical immutable document. The quote
reader accepts an explicit historical version only, never "latest" at Calculate.
Current is read before execution; the consumer retains that version through
every actual attempt even if a new configuration is published concurrently.
Unknown/malformed historical formats never fall back to the old algorithm.

GET /api/admin/quotations/current returns {data:Current}. POST
/api/admin/quotations/calculate accepts price_version/model/group and usage
with six optional canonical nonnegative int64 decimal string fields named
input_tokens/output_tokens/total_tokens/cached_input_tokens/cache_write_input_tokens/
reasoning_output_tokens, plus completeness/source/profile. Missing/null is
unknown, "0" is explicit zero. JSON numeric counts are rejected. It returns
{data:Quote}. The route calculates an administrator-supplied hypothetical
observation; it does not attest that the counts came from an executed request.
Unknown/repeated fields, duplicate keys, query parameters, trailing JSON and
bodies over 16 KiB are rejected. The new API does not initiate generation.

Every operation requires a current active administrator login session in both
HTTP middleware and L3 service. API Keys/legacy admin key cannot authorize it.
L3 reuses frozen SessionUser and the shared per-owner limiter (120/minute),
with a finite five-second read context; no session is retained. Errors expose
fixed messages only: 400 invalid/overflow, 401 unauthorized, 403 forbidden,
404 unknown version, 429 rate limited, 503 unavailable. Owner users cannot
query selling schedules through this new diagnostic endpoint.

## Selling Profile And Arithmetic

Price profile `responses_input_partitions_sale_v1` binds observation profile
`responses_native_tokens_v2`. It prices three disjoint input partitions:
uncached = input - cached - cache_write, cached, and cache_write. Output has
one uniform price including reasoning; reasoning remains an observed subset,
never an additional charge. No visible-text token inference is introduced.

All base counts and cached/cache-write must be known even if a configured
partition price is zero. Missing required quantities return Known=false,
AmountUnits="", Reason="usage_unknown". A mismatched known usage profile
returns unknown with reason="usage_profile_unsupported". An absent exact model
or group returns reason="price_missing" or "group_missing". All unknown cases
remain distinct from explicit zero, which returns Known=true, AmountUnits="0",
Reason="quoted". Malformed observations return ErrInvalid, never a zero quote.

Rates are canonical decimal integer strings in units of 1e-9 configured
currency per 1K tokens, range 0..1e18. Group multipliers are positive scaled
integer strings, scale1e9, maximum1e15. No wildcard model or group fallback.
Model/group identifiers are bounded nonempty UTF-8 without control characters;
models cannot contain '*'. Currency is explicitly configured safe ASCII
metadata (1..32 characters), never inferred from provider cost or exchanged.
Quotation.Enabled=false is the default and does not record any new document.

Compute with wide integers:
ceil(((uncached*U + cached*C + cache_write*W + output*O)*groupRate)
     /(1000*1e9)).
Round upward once after summing and applying the multiplier. The final amount
must fit nonnegative int64; intermediate products use math/big. UnitScale is
the string "1000000000". Input partitions are not added on top of total input.
A quote is a user selling calculation; it is neither a committed charge nor
an upstream cost bound, invoice amount, hold or permission to execute.

## History, Configuration And Lifetime

The independent canonical document uses format=2, kind="dimensional_quote",
price_profile/usage_profile/currency/unit_scale, sorted exact price rows and
canonical group multipliers. It shares the existing content-addressed immutable
history table, but only the new reader interprets this kind. Existing format1
payloads and digests are unchanged and continue to use the old reader.
No existing reservation refers to the new document. Append-only history is
retained independently of ordinary request-log cleanup.

Root quotation YAML is startup-only and opt-in: enabled, currency, prices
(model plus uncached_input_units_per_1k/cached_input_units_per_1k/
cache_write_input_units_per_1k/output_units_per_1k strings), group_rate_units.
Limits are 256 model rows, 64 groups and document <=256KiB. An enabled config
requires at least one exact model and group; no price or currency defaults.
Main persists a validated document before exposing its current version. A
failed append does not publish it. Runtime Replace used by the owner is
serialized; there is no management mutation endpoint or extra polling worker.
Historical reads use the supplied context, verify version/digest/kind/bounds
and do not create an unbounded in-memory version cache. Restart can quote old
versions from the existing database. Dependencies close through existing main
lifetime; this capability starts no goroutine and performs no upstream I/O.

The independent L3 sample captures current version, executes native HTTP/SSE
once through the real gateway, and calculates per-attempt quotes using that
version after provider observations. Tests publish N+1 during the actual
upstream request and prove N still applies, then reopen the real SQLite store
and read N again. It must not alter used/reserved balances or retry generation.
Amounts are per-attempt quotations; there is no new aggregate settlement policy.

## Alternatives, Attribution And Rollback

Mutating format1 or adding fields to frozen configuration/ledger DTOs was
rejected because strict crash recovery reads those exact histories. Durable
request observations are deferred to R05a-3: they need trusted invocation IDs,
attempt idempotency, explicit failed-write policy, retention and owner queries.
The lossy RequestLog queue cannot be represented as a reliable usage ledger.

Existing TXAPI fixed-point snapshot/append-only patterns are reused through
their owners. The codex-proxy-rs separation of provider counts and computed fees
and sub2api pricing partitions inform design only; no new upstream source or
dependency is copied in this batch. R05a-1 source notices remain applicable.
No provider retail rates are supplied or claimed current by this implementation.

Rollback disables quotation at startup after requests drain. Existing old-price
settlement/recovery and persisted versions remain intact. New quote documents
are retained but never fed to old settlement. Unknown/new-format errors remain
explicit. Real Provider/SDK, R05b complete cost/charged background, OAuth and
external Runtime gates stay independent. This batch does not freeze the new
candidate contract or complete all R05a/R00-R07.
