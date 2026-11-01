# Network proxy boundary

`internal/harness/proxy.go` is the per-request network boundary. It is the
process that should own real egress once `ACTIVE_HTTP` is enabled; `Egress` is
the client-side grant check, not the final boundary.

```text
Tool
  │  only an Egress bound to an ExecutionGrant
  ▼
Egress ── direct ──► public IP check (no proxy configured)
  │
  └── ApprovedProxy ──► local proxy
                          │  NetworkPolicy per request
                          │  AuditSink (decision first)
                          ▼
                        target (public-IP filtered, body-limited)
```

## Policy input and decisions

```go
type Request struct {
    Protocol Protocol // http | https_connect
    Host     string
    Port     int
    Method   string   // empty for CONNECT
    Path     string   // escaped path, no query
    Client   string
}

type NetworkPolicy interface {
    Decide(ctx context.Context, req Request) (PolicyDecision, string)
}
```

`PolicyDecision` is `Allow`, `Deny`, or `Ask`. There is no approval UI inside
the proxy, so **`Ask` is treated exactly like `Deny`** (403). A future
interactive layer can map `Ask` to a human review before returning a decision.

An unknown decision, a nil policy, or a policy error is `Deny`. The proxy never
falls back to direct egress.

## HTTP forwarding

- Only absolute-form requests are accepted (`GET http://host/path`).
- Policy is evaluated on host/port/method/path before the body is read.
- The decision is written to `NetworkAuditSink` before forwarding.
- If an `Allow` decision cannot be audited, the proxy returns 502 and does not
  forward. An un-auditable request is not allowed.
- Request and response bodies are capped (default 8 MiB); oversized bodies are
  rejected rather than truncated.
- Hop-by-hop headers and every header named by `Connection` are stripped.
- The dedicated transport never consults `http.ProxyFromEnvironment`.

## CONNECT

- `CONNECT host:port` is evaluated as `https_connect` with host/port; method and
  path are unknown and therefore not policy-checked.
- `Allow` hijacks the connection and tunnels bytes; `Deny`/`Ask` returns 403.
- Target dialing uses `publicDialContext`, so loopback/private/link-local/ULA
  targets are rejected unless the test-only `AllowLoopback` is enabled.
- Bytes already buffered by the hijacked `bufio.Reader` are forwarded before the
  raw socket, so a client that pipelines the TLS ClientHello after CONNECT does
  not lose it.

This is an opaque TCP tunnel: there is **no TLS MITM**, so a proxied HTTPS
request's method/path is not visible to the policy or audit. Enabling
method/path policy for HTTPS requires a managed MITM certificate and a separate
review.

## Audit

```go
type NetworkAuditSink interface {
    RecordNetworkDecision(ctx context.Context, event NetworkDecisionAudit) error
}
```

The event carries timestamp, protocol, host, port, method, path, decision,
reason, client and policy version. It never carries request/response headers or
bodies.

`AuditSink` adapts it to `audit.Event` (`network.policy_decision`). The raw path
is replaced by `path_hash` because a path may contain callback tokens or long
digit runs that `audit.Event`'s secret scanner would reject. A nil auditor makes
`AuditSink` fail closed (the proxy then refuses `Allow`).

## Rules bridge

`RulePolicy` adapts `internal/policy/rules` to `NetworkPolicy`:

- rule `allow` → proxy `Allow`
- rule `require_human` → proxy `Ask` (403 today)
- rule `forbidden` → proxy `Deny`
- no matching rule / nil rule set → proxy `Deny`

The rule layer's program/action are bound when constructing `RulePolicy`; the
proxy supplies host/path/method per request.

## Egress integration

`Sandbox.ApprovedProxy`, when set, must be an `http(s)` URL with a host.
`NewEgress` then:

- sets `http.Transport.Proxy` to that URL;
- dials the proxy itself with a pinned address, allowing the operator-approved
  proxy to be loopback/private;
- keeps `publicDialContext` for any direct (non-proxied) connection;
- fails closed if the proxy URL is invalid (it never silently falls back to
  direct egress).

The proxy is the only component allowed to reach a target when it is configured;
the execution environment should additionally enforce that the tool process can
only reach the proxy (network namespace / firewall / AppContainer). That OS
boundary is not implemented in this repository yet.

## Daily budget and concurrency

- `ExecutionGrant.DailyLimit` is enforced by a `dailyCounter` shared per program
  on the base `Egress`; two separate `WithGrant`/`Execute` calls cannot each
  reset the platform's daily ceiling.
- RPS and concurrency remain execution-local.
- A grant with no program/asset identity gets a per-grant counter (fail-safe,
  not shared).

## Deliberate limits (v1)

- No client authentication; `Client` is `RemoteAddr` only.
- No TLS MITM; CONNECT is opaque host/port tunneling.
- Request and response bodies within the cap are buffered in memory.
- `NetworkAuditSink` is an interface; production must wire `AuditSink` to a
  durable audit store.
- No interactive approval flow; `Ask` is a 403.
- The proxy listener itself is a new trust boundary; bind it to loopback and
  keep `AllowLoopback=false` in production.
