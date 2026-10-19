# Versioned Selling Quotations

R05a-2 adds an opt-in, read-only selling calculation. It never reserves budget,
deducts a balance, changes an invoice or authorizes a model request. Quantity
observations, customer selling calculations, Provider spend and budget holds
remain separate facts. The candidate quotation/v1 contract does not freeze new
financial behavior. Frontend work remains deferred.

## Enable And Configure

Set quotation.enabled=true at startup with an explicit configured currency,
exact model prices and group_rate_units. There are no price/currency defaults,
wildcards, group fallbacks or Provider retail rates. Restart applies a new
configuration only after its immutable history is saved successfully. The
existing billing settings and old model price APIs remain independent.

Rates are canonical nonnegative decimal integer strings in units of 1e-9 of
the configured currency per 1K tokens, maximum 1000000000000000000. Positive
multipliers use scale 1000000000, maximum 1000000000000000. The currency label is
explicit metadata, not a promise of exchange or Provider invoice equivalence.
A maximum 256 exact model rows and 64 group multipliers compile into a document
bounded to 256 KiB. The disabled example configuration contains illustrative
ACCOUNT_UNIT values, not any Provider tariff.

The selling profile responses_input_partitions_sale_v1 binds quantity profile
responses_native_tokens_v2. Input is partitioned into uncached, cached-read
and cache-write. Output uses one uniform rate including reasoning. Reasoning
is never added as another charge and no visible-text count is inferred.

## Calculation And Unknowns

uncached = input - cached - cache_write
amount = ceil(((uncached*uncached_rate + cached*cached_rate +
                cache_write*cache_write_rate + output*output_rate)*group_rate)
              /(1000*1000000000))

Wide integer intermediates and one final upward rounding prevent partition
rounding errors. The final amount must fit nonnegative int64. Example: input 10,
cached 3, write 2, output 6 and rates 1000/100/2000/3000 with multiplier 1000000000
produce ceil(27300/1000)=28 integer ACCOUNT_UNIT nanounits. Reasoning 4 is already
included in output 6. It adds no second output fee.

All three base counts and cached/write are required, including when a rate is
zero. Missing/null stays unknown; explicit zero stays known. A Provider that
omits cache_write_tokens therefore may not have enough information for this
profile. Do not substitute zero or use a different profile implicitly. All
quantity validation, parent/transitive bounds and overflow checks apply before
calculation. Complete base usage alone does not promise available breakdowns.

Known=false returns amount_units="" and a fixed reason: usage_unknown,
usage_profile_unsupported, price_missing or group_missing. It is distinct from
Known=true/amount_units="0"/reason="quoted". Invalid observations, incompatible
historical documents and amount overflow return explicit errors rather than
free quotations. Amounts apply per attempt; no retry settlement sum is defined.

## Administrator HTTP Capability

GET /api/admin/quotations/current returns data with enabled, version, currency,
price_profile, usage_profile and unit_scale. POST
/api/admin/quotations/calculate accepts explicit price_version, exact model,
group and usage. Counts use canonical nonnegative int64 decimal strings; nil
fields are absent/null and "0" is known zero. Usage metadata includes
completeness, source and profile. All quantities stay exact through JSON.

The POST endpoint calculates supplied hypothetical quantities. It does not
attest that those counts came from an executed request. The version is a
content digest and is unrelated to the runtime desired/applied publication
revision. Historical versions are explicit; calculate never silently chooses
"latest". Requests reject duplicate/unknown fields, noncanonical quantity
strings, trailing JSON, query parameters and bodies over 16 KiB.

Both middleware and L3 refresh a current active administrator session for every
operation. API Keys, the old management key and ordinary-user sessions cannot
authorize these routes. The shared per-owner quotation bucket allows 120/minute,
with a five-second bounded read context. Successful responses set no-store.
Codes: 400 invalid/overflow, 401 unauthorized, 403 forbidden, 404 unknown version,
429 rate limited, 503 unavailable. Existing operations and schemas retain their
prior behavior. No new frontend or schedule mutation endpoint is included.

## History And Independent Consumption

Dimensional documents use canonical format 2/kind dimensional_quote in existing
append-only BillingPriceVersion history. New Reader alone interprets this kind;
old format 1 prices/digests and strict restoration remain exact. The new document
is never assigned to BillingReservation.PriceVersion or old settlement paths.
Ordinary request-log cleanup does not delete this immutable history. A document
is persisted before it is published, and a failed append retains the prior
current version. Reads validate kind, digest, bounds and canonical encoding.
No unbounded version cache, timer, external I/O or extra worker is added.

A consumer captures Current before a real generation, retains its version and
calculates from the resulting owned observations. N+1 publication during the
upstream request cannot reprice N. An independent L3 sample and SQLite restart
regression demonstrate this sequence. The sample must bind actual model/group
from the host for a single attempt. Existing v1 AttemptOutcome does not expose
each retry's group; final ChannelGroup must never be applied to older attempts.
Unsupported per-attempt binding is explicit until a later reviewed capability.
Quote failures after wire delivery do not alter that delivery or replay calls.

## Disable And Roll Back

Disable quotation at startup after requests drain. Current reports disabled and
Calculate is unavailable even if the database contains old quote documents.
Existing old-price settlement and crash recovery remain unchanged. New history
rows stay retained and never get fed into old settlement. No schema version is
added in this batch. Durable request observations, new billing dimensions,
complete Provider-cost proof and charged Responses/background/WS remain later
backend acceptance, and real Provider/SDK/OAuth gates remain independent.
