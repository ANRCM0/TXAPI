# R06c: Ledger Amount Meaning And Reconciliation Visibility

Decision recorded before source changes, 2026-10-19. Base is the tree-verified
R02 corrective merge master/252c3792de2786045500073f84a2161e6c911e54, matching
accepted head 135e657. Push 39193105666 and PR 39193153559 passed complete
check/delivery. Post-merge CI is tracked separately. The user has authorized
choosing subsequent bounded batches without repeated proposal approval; OAuth
account sources and acceptance remain explicitly closed by the user.

## Six-Part Boundary

1. L3 console presentation in web/assets/app.mjs, a focused display helper in
   web/assets/api.mjs if useful, existing Node/API/browser regression files and
   operating/implementation records. No L0, Go feature, schema or main change.
2. Consume frozen ledger/v1 and the existing administrator reservation list,
   detail and explicit resolution endpoints. All signatures and wire fields stay
   exact. API authority and can_resolve remain server-owned.
3. Do not change foundation, settlement, price snapshots, holds, attempts,
   provider contracts, recovery or financial mutations. Values remain decimal
   strings formatted through the existing BigInt unit conversion.
4. Distinguish final user charge, currently retained user budget, historical
   original reservation and independent provider budget/envelope observations.
   Provider envelopes survive settlement; they are never actual provider costs.
   Actual provider cost is explicitly unavailable in this contract. Pending or
   active rows have no finalized charge even if charge_units contains zero.
5. Preserve confirmation, explicit usage, no automatic resolution retry, audited
   final decision, historical tariff, unknown receipt amount, pagination failure
   retry and owner/admin navigation tests. Add state/waiver/zero-charge/old record/
   max-int64/desktop/mobile cases and exact source/contract checks. Concentrate
   local checks, then exact-head automatic full CI and merge-tree verification.
6. A single UI integration batch owns app/API helper edits. Test-only delegation
   may edit billing.cjs and API tests. No concurrent module/API/schema owner.

## Field Mapping

- reserved/active/pending -> current retained user budget equals reserved_units.
- settled -> current retained user budget is zero; reserved_units is historical.
- unknown state -> current retained amount and finalization stay unknown.
- settled -> charge_units is the final user charge, including explicit zero.
- other states -> final charge is not finalized; zero is not a free outcome.
- provider_held_units/provider_budget_units -> cumulative conservative envelope
  and request budget; never cleared or subtracted as if they were a user hold.
- attempts.cost_known/cost_units -> received usage priced by the selling tariff;
  unknown remains unknown. This is neither an additional charge nor an invoice.
- resolution -> immutable explicit administrator decision; waive does not imply
  free provider execution. No page-derived ability replaces can_resolve.

Lists separate these values by purpose and retain state/reason. Details show
original reservation and current retention separately, distinguish tariff-priced
usage from final charged amount, keep contract/tariff snapshots and label actual
provider costs unavailable. Existing token_id list filtering can be surfaced.
No cross-page totals, profit estimate, fabricated currency or new pricing mode.

User charges/holds retain their account pricing unit. Provider envelope/budget
values retain the recorded contract currency, with no implied conversion or
comparison. New provider tariff profiles being USD does not certify the currency
of older records. The UI must not label all amounts as one configured currency.

## Compatibility And Rollback

Existing endpoints, fields, SQL and financial semantics remain exact. No data
migration or new public signature. Rollback restores previous UI assets; it does
not alter balances, reservations, decisions, provider calls or recovery. This
page does not complete S5 cost proof, R05 billing expansion or full R00-R07.

## Concrete UI Projection

Add ledgerAmounts(row) in the existing local ES module. It returns display
strings named originalReservation, retainedBudget, finalCharge, providerEnvelope
and providerBudget. Decimal string amounts use ledgerMoney/BigInt. A missing,
invalid or non-string amount is Unknown, never a guessed zero. Known states
reserved/active/pending retain reserved_units and show Not finalized charge.
Settled shows retainedBudget 0 and the recorded final charge, including 0.
Unknown states show Unknown for both current retention and final charge.
Original and provider fields remain independent recorded values in all states.

The list labels Final user charge, User budget retained, Provider envelope /
budget and State separately. Detail labels Original user reservation, Current
retained user budget, Final user charge, Cumulative provider envelope and
Provider request budget use the same projection. Actual provider cost is Not
recorded. Attempt cost is Usage at selling tariff; cost_known=false remains
Unknown (not free). Existing snapshots and explicit resolution controls remain.
Expose the existing token_id filter as Billing Key ID alongside Billing user ID.

One agent owns tests/browser/billing.cjs and web/assets/api.test.mjs. The
integrating agent owns app.mjs, api.mjs, any scoped CSS and documentation. No
shared file writes. Only focused risk regressions are added; old cases remain.

## User Priority Update

The user clarified backend completion first and deferred frontend work. This
locally verified batch is retained on feat/reference-r06-ledger for later
integration; no remote CI/merge acceptance is claimed. Subsequent frontend work
should preferentially borrow codex-proxy-rs page/interaction/component mechanisms
and adapt them to the established TXAPI stack with provenance and regression
checks. Backend R05a work starts separately on the accepted master baseline.

## Integration Resumed

On 2026-10-19 the user requested consolidation of the preserved branches into
master. This resumes only this existing ledger presentation slice. It is ported
onto master/290ba5d alongside the independently scoped R04a source integration;
the original backend-first checkpoint above remains historical. No additional
page expansion, financial authority, HTTP fields or currency conversion is added.
The current API/desktop/mobile regressions and final CI/merge evidence are
recorded in [the integration record](docs/history/branch-integration-20261019.md).
