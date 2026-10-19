# R04a: Referenced OpenAI Account Authorization Tool

Decision recorded on 2026-10-19 before executable source changes. Base is
accepted master/bdcb307e18b029119f56f2cd4c8989318e60e51d, tree
8a97d47ed4fd19c332c0bb20f130f06096d9f55f. The user explicitly instructed
direct adoption of relevant reference-project implementations after the prior
entry-gate audit. Earlier autonomy approval remains in design section 6.

This authorizes a concrete OpenAI source/controlled-validation increment before
an account is supplied. It does not open real-account acceptance, paid execution
or external Runtime gates. The deferred frontend remains deferred.

## Six-Part Implementation Boundary

1. Layers/directories: additive mechanical raw HTTP in L0 core; candidate L1
   upstreamauth/v1 contracts, L1 upstreamidentity coordinator and foundation
   upstream HTTP bridge; pure L2 protocol/openaiauth and L2 loopback callback;
   existing store/model and the cmd/gateway assembly. CLI is the actual operator
   consumer. No L3 network/storage implementation is introduced.
2. Dependencies: preserve existing frozen execution/identity/control contracts.
   Add one explicitly candidate upstreamauth/v1 contract for bounded wire calls,
   provider preparation/verification, callback ownership and durable accounts.
   Existing signatures remain exact. New L0 declarations and the candidate
   contract are recorded in the signature snapshot only after real consumers.
3. Foundation: existing core forces application/json and converts/discards
   non-200 bodies before decoder access. Its public generation executor is an
   authorized model-operation contract, not an OAuth request primitive. A small
   raw call on the same core.Executor/client owns one bounded HTTP attempt,
   original status/header/body and conservative dispatch observations. Existing
   Execute/ExecuteTo behavior is unchanged. No second HTTP stack, retry loop,
   routing, billing, OAuth or account policy is added to L0.
4. Contract: explicit local operator commands authorize/reauthorize, list,
   refresh once and disable. Authorization starts its bounded loopback listener
   before showing a URL. OpenAI dynamic registration, S256 PKCE, one-use state,
   nonce, issued-client binding and signature-verified OIDC identity are required.
   Separate encrypted managed accounts do not become static API Keys or eligible
   routing credentials. Read output contains metadata only.
5. Verification: preserve regressions; target callback/PKCE/JWT attacks, actual
   SQLite encryption/CAS/claims/restart/overflow, cancellation and uncertain
   refreshes, old-schema upgrade/reopen/rollback and actual command consumers.
   Once stable, run one local full batch, final exact-head complete automatic
   CI, authorized merge/tree comparison and independent main CI. Simulated
   authorization is explicitly not real OpenAI authentication acceptance.
6. Parallel impact: root owns contracts, L0/bridge/coordinator, CLI, assembly,
   registry/snapshots, migration allocation, docs and integration. Protocol and
   store tasks receive isolated files only after the concrete contract exists.
   No workflow, frontend, existing financial authority or inference route edit.

## Fixed Source And Direct Adaptations

Reference codex-proxy-rs, revision
591f95cc18ce95eb6fc87cf46a3526c5b3ecba36, Apache License 2.0:

- `backend/crates/providers/openai/src/credential/oauth.rs`: random_secret,
  authorization_url's S256 derivation, callback_parts,
  set_unique_callback_parameter and constant_time_equal. Port the mechanics to
  Go, current protocol fields, exact loopback target checks and bounded lifetime.
- `backend/crates/providers/openai/src/credential/token_client.rs`: bounded
  form token exchange and fixed error categories. Preserve no raw token/body
  diagnostics; full successful token replies must validate before persistence.
- `backend/crates/providers/openai/src/credential/repository.rs` and
  `refresh.rs`: independent network work, single-account claims and revision CAS.
  Reuse TXAPI's SQLite transactions/vault rather than its Redis/Postgres stack.
- `backend/crates/gateway-core/src/provider_ports/refresh.rs`:
  provider_refresh_backoff_at/provider_refresh_retry_at/stable_factor for bounded
  deterministic backoff after a proved not-dispatched refresh only.

The existing license copy remains in licenses/codex-proxy-rs-Apache-2.0.txt.
THIRD_PARTY_NOTICES.md records exact adapted files/functions and changes.
The fixed reference has no root NOTICE. No sub2api LGPL implementation is copied.
Reference recovery logs containing AT/RT and retrying ambiguous rotating-token
refreshes are excluded. Secrets never enter ordinary logs or recovery files.

## Current Protocol And Deliberate Source Differences

Official primary sources actually read on 2026-10-19:

- https://developers.openai.com/siwc/token-sharing-open-source/sign-in
- https://auth.openai.com/.well-known/openid-configuration

The sign-in subpage and live OIDC discovery supersede the older overview and
the fixed reference's desktop client/wrapper parameters. Authorization uses
https://auth.openai.com/api/accounts/authorize; token exchange uses
https://auth.openai.com/api/accounts/oauth/token. Bootstrap client_id is
dynamic_agent_client with stable persisted ext_agent_host_id and agent_name_hint
TXAPI on initial registration only; its wire host value is
urn:uuid:{persisted UUID}. The callback's unique client_id is used for exchange and saved for
later refresh/reauthorization. It is never replaced by the bootstrap ID.

Use response_type=code, scope openid profile email offline_access resource.invoke
chatgpt.tokens.use.direct,
resource=https://api.openai.com/v1, one-use state, nonce, S256 challenge and
http://127.0.0.1:{ephemeral-port}/auth/callback. Use the literal loopback address
and the minimal current public flow; omit old desktop wrapper parameters. No connectors, API write
permissions, special client impersonation, token exchange grant, userinfo,
inference, device flow or automatic browser launch is part of this slice.

ID tokens require RS256 against the bounded official JWKS, exact issuer
https://auth.openai.com, expected issued audience, subject, expiry/not-before,
initial nonce and optional access-token hash. Granted scopes come only from the
token response, are bounded, persist with their known/unknown state and never
come from the browser callback. This slice makes no inference-permission claim.
Never use decoded unverified JWT
claims as account authority. New refresh ID tokens are verified too; no ID-token
reply preserves the prior identity, and omitted RT is retained only after an
otherwise completely valid successful refresh. Unsupported or malformed values
fail closed. The official endpoints/JWKS are fixed; no production endpoint or
issuer override is added for tests.

## Durable Account And Refresh Contract

Append schema 17 through the one existing migration runner. Managed accounts
have random stable IDs, provider/verified issuer/subject/client binding, separate
positive int64 row and credential revisions, encrypted token envelope, expiry,
refresh availability, state, fixed failure category, next retry time, fencing
generation, hashed claim and a finite lease/started marker. Never reinterpret
historical static credentials or financial records as OAuth accounts.

Use the existing credential key file and AEAD ring with a separate account AAD
domain bound to account/provider/client/credential revision. Encrypted envelope
identity/expiry must match metadata. Startup verifies all retained accounts and
reseals old active-key ciphertext without changing logical revisions. Missing or
wrong keys reject startup. List operations explicitly select metadata only.
Capacity is finite (1000 accounts; 100 per page; four claimed refreshes).

Refresh is claim -> durable started -> one network call -> CAS finish; network
never runs in a DB transaction. A claim alone returns no RT. Start commits its
started marker before returning secret material. Disable/reauthorization revokes
claims and advances fencing. Stale replies cannot overwrite administrator work.
Revision/fence overflow fails closed. Reauthorization preserves stable account
identity; switching issuer/subject/client requires a new account.

Not-started expired claims can be reclaimed. Started expiry, partial I/O,
malformed/success-but-unpersisted replies and other possible RT rotation remain
uncertain and block reuse of the old RT. Proved not-dispatched work can release
the claim with bounded persisted backoff. A complete invalid_grant/invalid_client
requires reauthorization. Neither an HTTP error nor a retry flag proves that a
rotating RT was not consumed. There is no automatic refresh worker in this
operator-only increment, no network replay and no plaintext recovery log.

CLI operations use OS/operator access to the existing configuration/database,
like existing local bootstrap commands. They require actual credential encryption
for secret operations. Reauthorization fences the old material before browser
work; interruption leaves it requiring reauthorization. Callback failures do not
replace the account. One local flow times out in ten minutes and closes/join its
listener. Listing and finishing use finite independent DB contexts where needed.

## Compatibility, Rollback And Acceptance Status

Normal startup still uses the existing static Key path. No OAuth account enters
the scheduler, billing, model allowlists or inference transports in this slice.
OpenAPI and public HTTP routes remain unchanged. The operator capability and new
schema do not imply full R04/R06 management, real Provider compatibility or S5.

Stop writes and back up before schema 17. Feature rollback means stop using the
new operator command; schema16 binaries must refuse schema17. Binary downgrade
requires restoring a compatible stopped backup, never deleting accounts or
rewriting migration history. Retained token material remains encrypted.

At this pre-implementation checkpoint nothing new is implemented or verified.
Append actual source, commands, failures/corrections, source identity and final
CI/merge evidence in REFERENCE-ADOPTION-PROGRESS.md. Real-account end-to-end
acceptance remains pending until a lawful usable account is available.

## Preserved Branch Integration

On 2026-10-19 the user explicitly requested integration of the preserved branches
into the repository's actual master branch. The original R04a worktree was
snapshotted without changing its files or real index, then brought onto accepted
master/290ba5d with the separate R06c ledger presentation batch. The six-part
R04a source boundary above remains its own scope; the user's integration request
also resumes the previously deferred R06c slice.

The raw call and upstreamauth/v1 signatures are recorded as additive candidate
surface; existing declarations remain exact. A real operator command regression
crosses callback, PKCE/form exchange, signature verification, SQLite encryption,
refresh rotation, uncertain-result refusal, reauthorization and disable. Provider
HTTP is simulated; this does not certify a real account or inference permission.
Current official registration/sign-in documentation was fetched again for this
integration. Retained ID-token hints are not sent on reauthorization; the documented
account selector flow remains possible and verified identity must still match.

Commands, source disposition, failures and final integration evidence belong to
[the integration record](docs/history/branch-integration-20261019.md); operator
usage and schema17 rollback are in [the account guide](docs/reference/upstream-accounts.md).
