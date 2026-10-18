# R03b-2: Native WS Product Integration

Status: implementation decision recorded before source changes, 2026-10-19.
The user authorized autonomous design decisions after explicitly approving
R03b-1. This document is not a claim of acceptance or a new freeze.
Base: master/b74ef4acea70716ebe6c5b9b73c70c5dfcc224c8. R03b-1 functional
head 08c4c28 and merged master/0ec80e0 passed full CI. Documentation head
87c3ee7 and merged master/b74ef4a also passed full CI.

## 1. Batch Boundary

1. L1 writes: foundation/contracts/responsesws/v1, gateway native session and
   resource publication; access/rate/capacity remain their existing owners.
2. L2 writes: compat/responsesws codec additions, compat/responseswshttp,
   transport/websocket close-status support and server GET registration.
3. L3 writes: internal/responsesws client-round coordination. It imports only
   public contracts, never gateway/core/transport/store or another feature.
4. Assembly/config/contracts: cmd/gateway, internal/config, example YAML,
   OpenAPI and architecture inventory/snapshot are single-owner changes.
5. Dependencies: existing identity/v1 Authority/Authenticator, admission/v1
   RequestGate, execution/v2 Connection/Exchange, lifecycle/v1 Drainer. New
   responsesws/v1 is not frozen; existing public signatures remain unchanged.
6. No database migration, billing hold, persistent body/cache, OAuth, hosted
   tools, multiplexing, background generation or automatic retry is added.

## 2. Public Contract

New package internal/foundation/contracts/responsesws/v1 uses context, time,
identity/v1 and execution/v2. Its complete initial declarations follow.

~~~go
const Version = "1"
type Limits struct {
    MaxMessageBytes int64
    IdleTimeout time.Duration
    RoundTimeout time.Duration
    MaxLifetime time.Duration
}
type Problem struct {
    Code string
    Message string
    CloseCode uint16
    RetryAfterSeconds int
}
type Open struct {
    Binding identity.Binding
    ConnectionID execution.ConnectionID
}
type Create struct { JSON []byte }
type Result struct { Facts execution.ExchangeResult; Problem *Problem }
type Session interface {
    Execute(context.Context, Create, execution.FrameDelivery) Result
    Done() <-chan struct{}
    Problem() *Problem
    Close(context.Context) error
}
type Service interface {
    Open(context.Context, Open) (Session, *Problem)
    Limits() Limits
}
type ProblemDelivery func(context.Context, Problem) error
type Client interface {
    Run(context.Context, Session, execution.Connection, ProblemDelivery) *Problem
}
~~~

Compared with the earlier Service candidate, Done observes expiry/drain
without exporting authenticated context or implementation resources. Client
Problem returns a copied safe terminal reason after expiry or drain.
is the L3 coordination port consumed by L2; ProblemDelivery leaves JSON and
close frames with L2. Open happens before upgrade and must refresh a trusted
binding, not accept a constructed Principal as authentication.

## 3. Session And Protocol

- Default responses_ws.enabled=false, models=[], origins=[]; enabling requires
  billing.mode=disabled, explicit non-wildcard actual models and activated
  credential encryption. It does not require the HTTP Responses history runtime.
- One physical upstream connection per session, lazily opened on its first
  admitted round. Endpoint is fixed wss://api.openai.com/v1/responses in main.
  Only official OpenAI channel URLs qualify; fixtures substitute the assembly
  dial callback, never a production YAML switch for arbitrary upstream URLs.
- First target comes from the existing qualified/weighted selection owner.
  Pin channel/credential IDs, credential generation/material, actual model,
  upstream connection ID and resource revision. No account probing or failover
  occurs on later rounds. Every round refreshes identity, checks model policy,
  billing mode and current target eligibility, then reuses rate/request/attempt
  admission. A changed model, credential, account or resource revision requires
  reconnecting. A round already admitted can finish on its pinned old revision.
- Keep only the latest successfully delivered completed response ID and its
  session/model/target/lifetime binding. Accept previous_response_id only when
  it matches that ID; reject unknown, old, foreign or expired handles before
  sending. Reconnect/restart clears it. No stored-response fallback.
- Reuse the text/function codec. generate:false remains explicitly unsupported
  until its real terminal schema is confirmed. Unknown/media/hosted events,
  binary input, background, stream, non-main lanes and store!=false fail closed.
- TPM uses the existing text estimator, not a cost proof. A continuation adds
  the previous provider-reported total to new input estimation; when that
  history count is unknown, a TPM-limited continuation is rejected. Finish
  uses verified total usage, or retains the estimate on uncertain dispatch;
  a known non-dispatch refunds it. NewGenerationContext prevents stale RPM/TPM
  reservations from being reused across rounds. User RPM/concurrency and Key
  concurrency continue through the existing capacity owner.

## 4. Resource Publication And Shutdown

- A native resource generation owns an immutable compiled channel snapshot and
  a real bounded transport manager. Prepare receives a 10-second preparation
  context; resource lifetime belongs to the service root, not that temporary
  context. Candidate/retirement/failed-close tracking reuses R03b-1's private owner.
- The production management apply callback calls the native owner's Publish.
  All candidate validation/capacity checks precede the existing rollback-safe
  gateway.Publish callback; the final native pointer assignment cannot fail.
  WS selection reads the pinned generation snapshot, not a separately updated
  registry. HTTP and WS are not two independently fallible publications.
- Global logical sessions <=64, user<=4, Key<=2. Downstream manager <=64; each
  generation upstream manager <=64, but every upstream connection is backed by
  one global session lease, so aggregate active/pending upstream sockets <=64.
  Message/reader slots and codec copies are bounded by session and message limits;
  this is not a measured 32 MiB whole-process heap guarantee.
  One active round, pending=0; L3 has one bounded reader and one worker path.
- Limits: 256 KiB message; 10-second handshake/write; 60-second idle; 120-second
  round; 55-minute connection lifetime; 130-second shutdown budget. Reader
  stays alive during execution to detect disconnect/control frames and reject
  busy requests without spawning unbounded work.
- Stop rejects new opens/rounds/publications. Close cancels and joins active
  sessions, closes upstream sockets, releases leases, drains generation close
  workers and joins downstream handlers before gateway/limiter/store close. A
  timeout is not successful drain; main keeps dependencies alive and retries
  joining rather than continuing into deferred dependency cleanup.

## 5. HTTP, Errors, Compatibility And Validation

GET /v1/responses coexists with POST. Disabled returns 404. Native clients
must use Authorization; query credentials and cookie authentication are not
supported. Origin is same-origin or an exact configured allowlist. Never
forward client authorization/cookie/proxy headers upstream. IDs are generated
server-side independently of X-Request-Id. Error JSON and close codes follow
REFERENCE-R03B-CHANGE-REQUEST.md; peer/provider details are not serialized.

Add OpenAPI 101 and failure responses plus WS message/close-code extensions,
configuration examples and client fixtures. Old HTTP/SSE/background behavior
and frozen declarations stay intact. Test real client -> registered server ->
controlled WS upstream, continuation isolation and rotation/revocation, busy,
limits, disconnect cancel/join, N/N+1 publication/failed prepare, management
desired/applied and shutdown dependency ordering. Include a real main assembly
fixture and prove HTTP inference count zero. Existing full static/tests/smoke
and exact-head automatic Linux race/container CI remain merge gates.

No legitimate real account has been provided; controlled fixtures do not prove
OpenAI endpoint/model or official SDK compatibility. Record those separately.
