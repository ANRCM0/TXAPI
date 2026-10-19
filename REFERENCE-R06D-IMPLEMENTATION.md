# R06d: Owner Financial Read Capability

Decision recorded before source, 2026-10-19. Base master/ef7a1da5660a661fd90067b757e54af15ec91c1a
is the accepted R05a-3 merge, identical to final head906f6e4. Final push39207672342,
PR39207678441 and independent main39208662187 all passed complete check/delivery. Backend-first and deferred frontend remain
the current user instruction. The prior chat explicitly authorized autonomous future choices: user message
01a1500a-c66a-75f2-8d7f-e665da3f5323, "Future changes do not require my approval; compare and choose the best option yourself" (translation).
The current chat confirmed backend-first scope. The new contract remains
candidate and its concrete signatures are recorded before implementation.

Integration baseline updated after the separate corrective PR #39: original
local owner commit52cbe2c was rebased as04799e8 onto master/755128df7612a00bed5f62c91c6b34a96fbd3a1b.
That merge exactly matches final corrective headbb5a006/tree2f56eaeee85a222a9fcb740504c43891706d5b2a,
whose complete push39215267374 and PR39215271764 pass every check/delivery step.
Independent main39216287290 has now passed complete check/delivery with no
failed steps, verified separately before owner integration. New owner full checks run on this actual
merged source. Prior directed results remain tied to their earlier owner source.

## Six-Part Boundary

1. Store reads existing billing balances/reservations with current owner session
   checks in the same transaction. Billing remains the only balance/hold/charge
   writer. L3 ownerbilling consumes a new small read-only capability through the
   existing control and admission ports. Main and server integrate it.
2. Append ownerledger/v1 with Summary/List/Read Repository and Service. Retain all
   existing registered declarations, especially administrator ledger/v1. Reuse
   the existing store currentSessionScope(false), not an alternate auth workflow.
3. No L0, schema, price, settlement, reserve, recovery, payment or upstream send
   change. No real Provider assumption, paid background/WS or OAuth gate opens.
   No frontend source is changed; the old personal usage endpoint stays exact.
4. Three GET session operations append to OpenAPI. No mutation/replay/resolve
   method exists. Scope is always the current owner's own financial metadata,
   including when the current session belongs to an administrator. No supplied
   owner ID, privileged Engine or post-read filtering of administrator DTOs.
5. Real SQLite and the existing billing lifecycle prove known/null/zero,
   nonzero balances/holds, current permissions, old-price stability and no
   writes. Test actual HTTP/current sessions, cross-owner 404, revoked creation
   Keys, user deletion/revocation, exact decimal precision, invalid data and
   bounded reads. Final local checks and exact-head complete CI precede merge.
6. Integrator owns contract, architecture/snapshot, L3/API/OpenAPI/main/docs.
   Store owner receives the agreed contract and owns only the new reader/tests.
   Independent consumer/test owner receives explicit new test files. Existing
   frontend branch and administrator ledger implementation remain separate.

## Candidate Contract

Version="1"; ErrInvalid, ErrUnauthorized, ErrNotFound, ErrUnavailable,
ErrRateLimited. Access contains only Session string. Query contains State string,
TokenID uint and Limit/Offset int.

Summary contains UnitScale string, BalanceKnown bool, PostedUsedUnits/HeldUnits
*string, PendingCount string and AsOf int64 (Unix milliseconds). A missing user
balance yields unknown/null, not zero. PostedUsedUnits includes the original
opening imported from legacy used quota and is not an all-time invoice total.
It is never reconstructed from floating User fields, logs, quotations or usage.
PendingCount is a canonical nonnegative decimal string from the same transaction.
Do not add user/key balances together or invent available quota/currency.

Reservation contains ID, OriginModel, PriceVersion and State strings, TokenID
uint, OriginalReservedUnits/HeldUnits strings, ChargeUnits *string, LeaseUntil
int64 and CreatedAt/UpdatedAt time.Time. Detail contains Reservation and an
optional ResolutionAction string. Page contains Data []Reservation and HasMore
bool. No raw reason/evidence/actor, target/model/group/channel/credential,
Provider budget/contract/spend, attempts or full price payload is read or exposed.

Repository and Service both provide:

```go
Summary(context.Context, Access) (Summary, error)
List(context.Context, Access, Query) (Page, error)
Read(context.Context, Access, string) (Detail, error)
```

## Exact Financial Meaning

The original reserved amount remains a historical ceiling after settlement.
Live HeldUnits equals that amount only for reserved/active/pending, and is "0"
for settled. ChargeUnits is null until settled, including pending records with
an underlying default zero; settled zero is a known "0" user charge. Lease
expiry does not release a hold. Settled/waived user charges do not prove known
Provider cost, invoice completeness or operation success. ResolutionAction may
expose only settle_usage/cap_usage/waive after a valid existing terminal decision.
Negative amounts or unknown/corrupt financial states fail with Unavailable.

Every SQL projection is a whitelist and every reservation predicate includes
the session-derived owner. Creation Key revocation, expiry, or deletion does
not prevent current owners reading their historical financial metadata. Session
logout, user disabling or soft deletion does prevent reads. A foreign token_id
filter yields no foreign rows. Nonexistent or foreign detail IDs return404.

## HTTP And Bounds

GET /api/me/billing/summary
GET /api/me/billing/reservations
GET /api/me/billing/reservations/:id

Summary and detail reject all query parameters. List accepts only state,
token_id, limit and offset; repeated/empty/unknown/noncanonical values reject.
State is empty/reserved/active/pending/settled. Defaults50,max100,offset<=10000.
Order created_at DESC,id DESC with limit+1 for HasMore. Pages and separate
requests do not claim one frozen global snapshot. Owner rate120/minute is
shared across sessions/methods; repository reads use a five-second context.
Every response is no-store; errors are fixed400/401/404/429/503 without SQL,
database paths, raw provider errors or financial mutation recommendations.

The existing user balance and exact reservation records remain queryable with
billing disabled. Missing historical price documents do not prevent reading
already committed charges; no price document or current price is used to
recalculate a historical amount. Schema16 rollback/backup restrictions remain.
