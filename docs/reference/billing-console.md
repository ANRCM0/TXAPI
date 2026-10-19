# Billing Ledger Amounts

R06c refines the existing administrator ledger presentation. It consumes the
existing frozen ledger/v1 and administrator reservation list/detail/resolve
operations. No schema, pricing, settlement or HTTP contract changes are added.
Exact implementation and acceptance evidence are recorded in
[the batch log](../../REFERENCE-ADOPTION-PROGRESS.md).

## Amount Meaning

| Console value | Meaning |
|---|---|
| Final user charge | The recorded final charge only when state is settled, including an explicit zero. Other known states are Not finalized. |
| Current retained user budget | This reservation's reserved amount while reserved, active or pending; zero once settled. It is not the user's aggregate balance. |
| Original user reservation | The historical reserved amount, which remains recorded after settlement releases the hold. |
| Cumulative provider envelope | Conservative cumulative attempt bounds, retained after settlement; not actual provider spending or a user hold. |
| Provider request budget | The recorded independent upper-bound budget for the request. |
| Usage at selling tariff | An attempt's received usage priced with the historical user tariff, not an extra charge or an actual provider invoice. |
| Actual provider cost | Not recorded by the current ledger contract. No invoice or margin is inferred. |

User charges/holds use the account pricing unit. Provider envelopes/budgets use
the recorded provider contract currency; no currency conversion, direct
comparison or inferred historical currency is supplied by the page.
All monetary wire values remain decimal strings with the existing unit scale.
The browser uses BigInt formatting; it does not convert them through Number.
Missing, malformed or non-string projection amounts show Unknown, never a
guessed zero. Unknown states also show Unknown for retention and final charge.
A known zero attempt amount is distinct from cost_known=false, which remains
Unknown (not free). A user-charge waiver does not prove free upstream execution.

## Queries And Decisions

The page defaults to pending reconciliation. State, user ID and the existing
Key ID filter query the current page of reservations; no cross-page total or
profit is calculated. Original tariff and provider contract snapshots remain
available in detail, together with the immutable administrator decision.

Pending retains the user budget until existing settlement or an authorized
final reconciliation releases it. Expired pending resolution remains controlled
by the server's can_resolve field and current administrator authority. The page
does not derive new financial permission from state or displayed amounts.

The existing resolution flow still requires the full billing ID, explicit usage
or waiver, reason and evidence. It sends one explicit command and keeps the
existing final-decision audit semantics. No automatic retry, new mutation,
budget authority or provider-cost guarantee is introduced.

## Scope And Rollback

The change is local console presentation and its behavioral regressions.
Rollback restores the prior assets without changing reservations, balances,
provider envelopes, historical prices, decisions or recovery. It does not
complete R05/S5 cost acceptance or enable charged Responses/background/WS.
