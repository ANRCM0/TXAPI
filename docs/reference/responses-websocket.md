# Native Responses WebSocket

R03b-2 adds a default-off product consumer. This document describes the
implemented subset, not provider or official SDK certification. Exact local
and CI evidence belongs to [the batch record](../../REFERENCE-ADOPTION-PROGRESS.md).

## Enable And Connect

Set `billing.mode: disabled`, enable credential encryption using
[the existing secret procedure](secret-storage.md), and configure:

```yaml
responses_ws:
  enabled: true
  models: ["your-confirmed-actual-model"]
  origins: []
```

Create an enabled OpenAI channel with an official `https://api.openai.com`
base URL, an API-key credential and an explicit public-to-actual model mapping.
The actual model must occur in `responses_ws.models`. No fixture endpoint,
proxy bypass or plaintext-credential exception exists in production config.
WS and HTTP Responses enablement are independent; no history key is needed
for this nonpersistent WS subset. Schema remains v15.

Connect using `GET /v1/responses` with `Authorization: Bearer <call-key>` and
the WebSocket Upgrade headers. The disabled route returns 404 before auth.
Query parameters, cookie credentials and multiple Authorization fields reject.
Browser origins must be same-origin or an exact configured scheme/host.
Browser JavaScript cannot normally set Authorization on its native WebSocket;
the origin allowlist does not add cookie or query authentication.

Send text JSON, for example:

```json
{"type":"response.create","model":"your-public-model","input":"Hello","store":false}
```

After `response.completed`, another create on that same connection may use
its latest response ID as `previous_response_id`. IDs from another connection,
older rounds, HTTP history, reconnection or process restart reject before send.
Function definitions/calls/outputs use the existing native text/function subset.
See [OpenAPI](../openapi.json) for message schemas and close-code extensions.

## Limits And Failure Semantics

- One upstream native connection per logical session, opened lazily after
  current authorization, request rate/concurrency and attempt admission.
- Only the official `wss://api.openai.com/v1/responses` endpoint is used.
  Client authorization, cookies and proxy headers are never forwarded.
- At most 64 sessions globally, four per user, two per Key; one active round,
  no queued rounds. A concurrent create reports `busy` without another attempt.
- Messages are at most 256 KiB. Handshake/write: 10 seconds; idle: 60 seconds;
  round: 120 seconds; lifetime: 55 minutes; initial drain budget: 130 seconds.
- Every round refreshes current Key/model authority. The admitted active round
  retains its snapshot; revocation takes effect at the next round boundary.
- RPM/TPM and generation capacity reuse existing owners. Continuations add
  prior verified total usage to the new input estimate. Unknown history with
  a TPM-limited Key rejects; this estimator is not a provider cost proof.
- No automatic resend, failover, reconnect, persisted body, stored-response
  fallback or background execution. Disconnect cancels and joins this round.
  A potentially sent request is never refunded as known unsent.
- Native wire events retain the supported representation; provider errors are
  sanitized. TXAPI errors use `type:error` and `error.type:txapi_error`.
  Policy/continuation closes use 1008, binary 1003, oversize 1009, upstream
  failure 1011, capacity 1013, drain 1001, and normal expiry 1000.

## Publication And Shutdown

Management save/apply uses the native resource owner when enabled. Failed
prepare leaves the saved desired revision pending and the old applied snapshot
running. Successful publication retains old transports until their leases end;
a previously bound session must reconnect before starting on a new revision.
HTTP and native WS do not have separately fallible publication commits.

If discarded or retired resource cleanup fails, the owner retains the resource
and its capacity slot. A subsequent serialized Apply retries each eligible
failed cleanup once and waits for any existing cleanup worker before preparing
another candidate. This recovery wait respects the caller context and is at most
10 seconds; main's complete publication call also has a 10-second context.
Failure or timeout leaves desired pending and current/applied unchanged. Cleanup
continues under the resource owner after a request timeout, and a later Apply
can recover. Current resources and pinned retired resources are never closed
by this step. No background retry loop or provider request replay is added.

Stop rejects new handshakes, rounds and publications, cancels active rounds,
joins sessions, releases leases and closes transport generations before shared
gateway/limiter/store dependencies. Drain timeout is an error, not permission
to close shared dependencies. Main retains them and continues joining.

## Validation And Non-Goals

Run `go test -count=1 ./cmd/gateway -run TestResponsesWS` and the existing full
checks. The assembly fixture uses real client/server/upstream WS, SQLite auth,
management publication and counters proving zero HTTP inference fallback.
The ordinary main/Compose smoke also checks the disabled route is 404.
The composition fixture is not a real OpenAI service or official SDK test.

Legacy/strict billing, OAuth, media/hosted tools, generate:false, multiplexed
lanes, durable/cross-transport continuation, payment and production provider
certification remain unsupported. Full A04 and S5 cost acceptance stay open.
The new responsesws/v1 port is a candidate, not a newly frozen foundation API.
