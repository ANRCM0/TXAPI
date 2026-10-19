# Owner Financial Reads

R06d adds a candidate ownerledger/v1 capability and three session-only HTTP
reads. It uses the existing strict financial ledger and current session scope.
The L3 ownerbilling service depends only on the registered control, admission
and ownerledger contracts. Store owns projections; billing remains the writer.
[Decision](../../REFERENCE-R06D-IMPLEMENTATION.md) records the exact boundary.

## Operations

| Method and path | Result |
|---|---|
| GET /api/me/billing/summary | data: exact strict user balance, hold and pending count |
| GET /api/me/billing/reservations | data array and has_more |
| GET /api/me/billing/reservations/{id} | data: reservation and optional resolution_action |

Bearer authentication requires a current login session. API Keys and the legacy
administrator key are not login sessions. Administrator sessions also see only
their own owner scope on these operations. No operation accepts an owner ID.
Store checks the active user, soft deletion and session in the same transaction
as each scoped query, even after the service's initial authorization. Creation
Key revocation, expiry or deletion does not erase the owner's financial history.
Current session logout or current user disabling/deletion blocks reads.

Summary and detail reject every query parameter. List accepts only state
(reserved/active/pending/settled), token_id (canonical positive signed-platform
ID), limit (1..100, default50) and offset (0..10000, default0). Unknown, empty,
repeated, signed or noncanonical numeric parameters fail400. Foreign token_id
filters return no foreign rows. Foreign, malformed and nonexistent detail IDs
all return404. List order is created_at DESC,id DESC, with one lookahead row.
Different pages and separate requests are independent snapshots.

Every response, including authentication/errors, is Cache-Control:no-store.
Reads have five-second contexts and share one owner120/minute limiter across
sessions and all three methods; rate rejection is429 with Retry-After:60.
Unavailable data returns a fixed503 without SQL, paths or raw Provider errors.
No financial mutation, settlement recommendation or retry-execution action is
exposed. Read-only access works with billing disabled.

## Financial Meaning

All money, holds and counts are exact nonnegative decimal strings. UnitScale is
1000000000 units per existing account unit; no currency or available balance is
invented. The user hold and the creation Key hold are never added together.

A missing strict balance has balance_known=false, posted_used_units=null and
held_units=null. It is not reconstructed from legacy floating used_quota,
request logs, usage metadata or current selling quotations. A known strict
posted_used_units includes the existing legacy opening import, so it is not an
all-time invoice total. PendingCount and balances use one SQLite read snapshot.

original_reserved_units is the historical reservation ceiling. held_units is
that amount for reserved/active/pending and zero after settlement. Lease expiry
alone never frees a hold. charge_units remains null until settlement, even if
the underlying pending column is zero; a settled zero is a known zero user
charge. A waived charge does not prove zero Provider cost, successful execution
or a complete Provider invoice. An optional resolution_action exposes only the
existing settle_usage/cap_usage/waive action after terminal resolution.

Old committed charges and price_version are returned as stored. New prices,
missing old price documents and application restarts cannot recalculate them.
No private Provider budget/contract/spend, attempt, target, credential, reason,
evidence, actor or full price payload is loaded into the owner DTO. Invalid
negative amounts and corrupt state/charge combinations return unavailable.

## Integration And Verification

Assembly injects ownerbilling.New(store.OwnerLedger(), existingControl,
sharedLimiter) into server.Deps.OwnerLedger. The new public port is candidate;
all prior25 registered package declarations remain unchanged. OpenAPI0.11.0
adds three operations and four owner DTO schemas; the old108 operations and
schemas remain exact. Schema16 and all existing financial writes remain.

Independent real SQLite/Engine consumers cover lifecycle states, legacy opening,
null versus zero, exact amounts above2^53, current permissions, creation Key
revocation, priceN-to-N+1, disabled restart, and full financial table snapshots.
A second real Store commits a resolution while Summary is in flight to verify
its transaction snapshot. HTTP tests cover scope, parsing, no-store and fixed
errors. Actual-main smoke covers empty/unknown owner reads and forced-kill
pending-to-waived metadata. Exact source, commands, CI and merge status belong
in the [batch record](../../REFERENCE-ADOPTION-PROGRESS.md).

No frontend is included in this backend-first batch. Existing personal usage
and administrator ledger APIs retain their scope. Provider evidence, full R05
cost closure, paid background/WS, OAuth and external Runtime remain separate
acceptance gates. To remove this additive capability, remove its assembly and
routes together; no financial schema rollback or backfill is involved.
