# R05a-1: Native Responses Token Breakdown Observations

Decision recorded before source changes, 2026-10-19. Base is accepted
master/252c3792de2786045500073f84a2161e6c911e54. Functional head 135e657,
push 39193105666, PR 39193153559 and post-merge 39193688299 all passed complete
check/delivery. The user prioritized backend completion, deferred frontend,
kept OAuth account acceptance closed and authorized borrowing mature reference
code. R06c is preserved only locally as feat/reference-r06-ledger/28fc3ec,
without push/PR/merge; future frontend work should prioritize codex-proxy-rs.

## Six-Part Boundary

1. L1 candidate execution/v3 value/decoder/executor contracts, private gateway
   validation/bridge and owned snapshots. L2 native Responses JSON/SSE decoder
   consumes the new capability. Existing L3 facts example demonstrates public
   v3 consumption through real identity, gateway and controlled HTTP/SSE.
2. Reuse frozen v1 invocation/output/identity and existing candidate v2 host
   facts/count values. All existing registered public declarations stay exact.
   New v3 is registered as candidate, not frozen. L0 stays byte-exact.
3. No new execution chain, transport, database, billing authority or financial
   permission. Capture optional cached input, cache-write input and reasoning
   output quantities as observations. Do not change pricing or ledger schema.
4. Exact new contracts are defined below. One encoded representation is delivered
   once. The explicit v2 projection is provided by L2; L1 does not branch on
   provider profile strings or parse provider JSON. Host checks its consistency
   with enriched counts and owns dispatch/commit/protocol facts.
5. Cover native JSON/SSE, missing/null/zero, strict integer and overflow checks,
   parent/child bounds, count consistency, monotonic cumulative snapshots,
   defensive copies, failures/cancellation/short writes and existing v1/v2/WS
   compatibility. Finish concentrated local checks and exact-head full CI/merge.
6. Integration owner owns contracts, validation/attribution, architecture and
   the independent example. Protocol agent owns only internal/protocol/responses;
   gateway agent owns only private gateway facts/relay/result fields/tests.
   No frontend, schema, pricing or shared main changes are scheduled.

## Candidate Signatures

```go
const Version = "3"

type TokenBreakdown struct {
    CachedInputTokens     *int64
    CacheWriteInputTokens *int64
    ReasoningOutputTokens *int64
}
type UsageObservation struct {
    Counts       v2.TokenCounts
    Breakdown    TokenBreakdown
    Completeness v2.Completeness
    Source       v2.UsageSource
    Profile      string
}
func (UsageObservation) Validate() error

type Envelope struct {
    Execution v2.Envelope
    Usage     *UsageObservation
}
type Decoder interface {
    DecodeUsageEnvelope([]byte) (Envelope, error)
    NewUsageEnvelopeStream() StreamDecoder
}
type StreamDecoder interface {
    DecodeUsageEnvelopes([]byte) ([]Envelope, error)
    EndUsageEnvelopes() ([]Envelope, error)
}
type AttemptUsage struct {
    Number int
    Usage  UsageObservation
}
type Outcome struct {
    Execution v2.Outcome
    Usage     []AttemptUsage
}
type Executor interface {
    Execute(context.Context, v1.Output, v1.Invocation) Outcome
}
```

Envelope.Execution owns the sole encoded delivery and the existing three-count
projection, including its existing profile/completeness semantics. Envelope.Usage
owns the enriched cumulative observation. This small compatibility duplication
keeps v1/v2 declarations exact and avoids provider-specific profile translation
inside L1. Both projections' three optional base counts must agree, including
unknown values; base-only completeness/source continue to follow v2 rules.
The new host facade is NewUsageFactsExecutor(g) v3.Executor; it invokes the same
executeTo once. Outcome.Execution preserves existing host facts. Outcome.Usage
uses their attempt numbers; every returned pointer/slice is owned and copied.

Gateway selects exactly one decoder capability: v3, then v2, then v1. A failed
v3 decode is a protocol failure, never permission to parse or deliver again
through another capability. Existing NewFactsExecutor and HTTP consumers keep
their old types and base profile projection. New host enforcement may reject
inconsistent enriched facts that older decoders did not interpret; native raw
representation and valid legacy behavior remain unchanged.

## Native Profile: responses_native_tokens_v2

Native fields are input_tokens, output_tokens, total_tokens,
input_tokens_details.cached_tokens, input_tokens_details.cache_write_tokens and
output_tokens_details.reasoning_tokens. No alias fallback, inferred total,
default-zero or provider-specific reasoning adjustment is introduced.

- All six counts are optional nonnegative int64 integers. Missing/null stays
  unknown; explicit zero remains known. Invalid known field types reject.
- Completeness is Complete when the three base counts are present and consistent;
  optional breakdown absence does not downgrade complete totals. Any known count
  without complete totals is Partial, including breakdown-only observations.
  All six unknown is Unknown with SourceUnknown and empty Profile.
- Any known count uses ProviderReported plus the declared profile. Nonempty
  profile IDs use at most 64 safe ASCII characters. A zero-value metadata enum
  does not silently become a validated unknown observation.
- Validate input + output without overflow; when total is known it must match.
  Known cached/cache-write lower bounds must fit known input, and their sum
  must not overflow. Known reasoning must fit known output. Subcounts never add
  to total, and output minus reasoning does not infer visible text tokens.
- Host cumulative validation retains a separate known-count high-water mark:
  later known counts cannot decrease even across intervening unknown snapshots.
  The observable snapshot still replaces missing fields with unknown; stale
  cached/reasoning values are not promoted to a later final count.
- Known child sums also supply lower bounds when their parents are unknown.
  An explicit total cannot be smaller than these input/output lower bounds;
  their checked sum must fit int64. The host applies this to cumulative history
  as well: old children constrain later explicit parents, while an old parent
  never acts as an upper bound on a later child with a missing current parent.
- Only the existing complete terminal base projection may feed core.Usage.
  Breakdown-only observations project to unknown v2 base usage. No new strict
  combination, price, charge or provider-spend permission follows.

## Reference Adoption And Source Evidence

Prefer a narrow Rust-to-Go adaptation of optional integer reading and checked
arithmetic from codex-proxy-rs at
591f95cc18ce95eb6fc87cf46a3526c5b3ecba36:
backend/crates/gateway-protocol/src/openai/events.rs, number_field,
nested_number_field and billable_usage_is_complete. Preserve the applicable
Apache-2.0 license text and identify adapted files/functions and modifications.
The upstream source has no file-specific copyright/SPDX header or root NOTICE;
do not invent an author declaration or change the entire project's license.

Changes from the reference are deliberate: keep nil rather than default zero;
never synthesize totals; use nonnegative int64 rather than u64; add reasoning
parent bounds, source/completeness and cumulative host checks. Existing TXAPI
SSE framing stays bounded and streaming; do not copy whole-response SSE parsing.

sub2api/b0a02a3304a0a78cdd003ddda1dd23ba02c03084,
backend/internal/service/openai_gateway_response_handling.go,
openAICacheReadTokensFromUsage/openAICacheCreationTokensFromUsage supplies a
behavioral reference for nested explicit zero taking precedence over aliases.
No LGPL implementation or gjson dependency is copied in this batch.

Official OpenAI guides were fetched on 2026-10-19 for the native field semantics:
https://developers.openai.com/api/docs/guides/prompt-caching
https://developers.openai.com/api/docs/guides/reasoning
https://developers.openai.com/api/docs/guides/token-counting
They define observation semantics, not real Provider or invoice acceptance.

## Compatibility, Deferral And Rollback

All old public signature entries and HTTP schemas stay exact. New v3 remains
candidate with a real native decoder and independent L3 consumer. No raw bodies,
credentials or unbounded extension maps are retained as facts. Existing WS keeps
its v2 path; no new WS profile or financial license is implied.

Pricing history, persistent breakdown records, management queries/display and
charged Responses/background/WS each remain later backend batches with their own
compatibility/cost gates. R05a-1 is not completion of all R05a or S5-03.
Rollback selects existing v2 decoding and removes the new facade/consumer after
requests drain; it does not replay calls or rewrite historical money/state.
