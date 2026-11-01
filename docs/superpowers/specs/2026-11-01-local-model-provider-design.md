# Local OpenAI-compatible Model Provider — Design

- Date: 2026-11-01
- Status: approved for implementation
- Scope: real `model.Provider` for a loopback OpenAI-compatible server, a store-backed
  `PolicyResolver`, and a `bountygrid model -call` CLI that actually invokes it.
- Non-goal: third-party/cloud providers, streaming, tool calling, reliability-planner
  integration, and any platform action.

## 1. Context

`internal/model` already contains the full router: data-class and egress policy,
local-first routing, budget, logical `ModelCall` + per-attempt `ModelCallAttempt`,
stable call IDs and error semantics (`FAILED_BEFORE_SEND` is the only retryable
state). Persistence exists (`model_calls`, `model_call_attempts`, `ModelCallStore`,
audit through `ModelCallAttempt`).

What is missing is a real `Provider` implementation and any CLI path that invokes one.
Today `bountygrid model` only prints a routing plan built from `StaticProvider`
catalog entries, and the `PolicyResolver` is an operator echo used only for tests.

The machine now runs a local OpenAI-compatible server (Strata, Qwen3.8-Flash-Next
IQ3_S) on `http://127.0.0.1:8080/v1`, which makes a loopback provider the natural
next increment.

## 2. Goals

1. A real `Provider` that talks HTTP to a loopback OpenAI-compatible server and
   maps usage/errors into the router's existing state machine.
2. A `PolicyResolver` whose egress ceiling comes from the stored
   `Program.ModelEgressPolicy`, not from request fields.
3. A `bountygrid model -call` mode that performs one real call and optionally
   persists `ModelCall`/`ModelCallAttempt` and the `model.call` audit event.
4. No change to the existing plan-only behavior of `bountygrid model`.

## 3. Non-goals

- Non-loopback endpoints. A non-loopback provider needs a different trust
  boundary (`LOCAL_NETWORK` with trusted CIDRs, or `THIRD_PARTY` with an
  escalation decision); that is a separate change.
- Streaming responses, tool calling, vision input, embeddings.
- Wiring the provider into `internal/reliability` or any worker loop.
- Any platform action. Model output remains text and never becomes a
  `PolicyDecision` or an execution request.

## 4. Package layout

| Path | Purpose |
|---|---|
| `internal/model/openai/provider.go` | OpenAI-compatible loopback `Provider` |
| `internal/model/openai/provider_test.go` | httptest-based unit tests |
| `internal/model/policy_store.go` | `ProgramStore` interface + `StorePolicyResolver` |
| `internal/model/policy_store_test.go` | resolver tests |
| `cmd/bountygrid/model.go` | `-call` mode and wiring |
| `cmd/bountygrid/model_test.go` | CLI tests |

`internal/model` stays free of `net/http`; only the `openai` subpackage imports it.

## 5. Provider design

### 5.1 Configuration

```go
type Config struct {
    Name             string        // default "local-openai"
    Model            string        // required; sent as the request model
    Endpoint         string        // required, loopback base URL
    APIKey           string        // optional; sent as Authorization: Bearer
    Timeout          time.Duration // default 120s; must be > 0
    MaxResponseBytes int64         // default 8 MiB; must be > 0
    MaxContextTokens int           // optional
    MaxOutputTokens  int           // optional
    Strength         int           // optional routing preference
}
```

### 5.2 Endpoint rules

- Scheme must be `http` or `https`.
- The host must be a loopback literal (`127.0.0.0/8`, `::1`) or `localhost`.
  Anything else is rejected at construction.
- Base normalization: strip trailing `/`; if the path does not end in `/v1`,
  append `/v1`; the request URL is `<base>/chat/completions`.
- Redirects are not followed (`http.Client.CheckRedirect` returns an error), so a
  loopback server cannot redirect the call to a public host.
- The client uses a transport with `Proxy: nil`. Local model traffic never goes
  through `HTTP_PROXY`/`HTTPS_PROXY`.

### 5.3 `Info()`

- `Trust = TrustInProcess`, `Local = true`, `Endpoint` = the normalized base.
- `ToolCalling`, `Vision`, `StructuredOutput` are false.
- Prices default to 0 (local calls cost nothing); the config can override them.
- `DataClasses` is left empty, which the router treats as "all classes". The
  loopback endpoint and `TrustInProcess` are what make `SENSITIVE` legal.

### 5.4 `Complete(ctx, req)`

Request body:

```json
{"model":"<Model>","messages":[{"role":"...","content":"..."}],"max_tokens":N,"stream":false}
```

Response mapping:

- `choices[0].message.content` → `ModelResponse.Content`
- `usage.prompt_tokens` → `Usage.InputTokens`
- `usage.completion_tokens` → `Usage.OutputTokens`
- `Model` and `Provider` are filled from the config; the router overwrites
  `Provider` with the registered name.
- `Content` is returned exactly as the server sent it. An empty content string
  with non-zero completion tokens is valid (for example, a small `max_tokens`
  consumed by reasoning) and is not an error.

Limits:

- The response body is read through a limit of `MaxResponseBytes + 1`; a larger
  body fails the call.
- The request context and client timeout both apply.

### 5.5 Error classification

The router treats a plain error as `AMBIGUOUS` and only
`domain.ErrNotSent` as `FAILED_BEFORE_SEND`. The provider must therefore prove
"not sent" conservatively:

| Condition | Result |
|---|---|
| Configuration/URL construction error before any request | `domain.ErrNotSent` |
| DNS/dial/TCP connect failure (`net.OpError`, `Op == "dial"`), connection refused | `domain.ErrNotSent` |
| TLS handshake failure before the request is written | `domain.ErrNotSent` |
| Context deadline exceeded after the request was written | plain error (`AMBIGUOUS`) |
| Any HTTP status other than 200 | plain error (`AMBIGUOUS`); status and body prefix may appear in the error, never the prompt |
| Response over the limit, malformed JSON, no choices | plain error (`AMBIGUOUS`) |

No prompt content or response content is placed in errors or logs. Errors carry
the provider name and, for HTTP errors, the status code and a bounded body
snippet (first 256 bytes, truncation-marked).

## 6. Policy resolver design

```go
type ProgramStore interface {
    GetProgram(ctx context.Context, id domain.ProgramID) (domain.Program, error)
}

type StorePolicyResolver struct {
    Store       ProgramStore
    Attestation ModelPolicy
}
```

`ResolveModelPolicy`:

1. `req.ProgramID == ""` → `ModelPolicy{Egress: LOCAL_ONLY}` (no escalation, no
   redaction); this is the fail-closed local path.
2. Store is nil → error (`model: program store is not configured`).
3. Load the Program; on error return the error (the router fails closed).
4. Return `ModelPolicy{Egress: program.ModelEgressPolicy.Effective(),
   Redacted: Attestation.Redacted, Escalation: Attestation.Escalation}`.

`Redacted` and `Escalation` are operator attestations supplied by the CLI, never
read from `ModelRequest`. Request fields remain hints and stay ignored by the
router.

## 7. CLI design

`bountygrid model` keeps its current behavior unless `-call` is set.

New flags:

| Flag | Meaning |
|---|---|
| `-call` | perform one real call instead of printing a plan |
| `-endpoint` | base URL; default `$BOUNTYGRID_MODEL_ENDPOINT` |
| `-provider-model` | model name; default `$BOUNTYGRID_MODEL_NAME` |
| `-provider-name` | registered provider name; default `local-openai` |
| `-api-key-env` | env var holding the bearer token; empty means no auth header |
| `-prompt` | user message text |
| `-prompt-file` | read the user message from a UTF-8 file (mutually exclusive with `-prompt`) |
| `-program` | ProgramID whose egress policy governs the call |
| `-step` | optional task step name, used for a stable call ID |
| `-timeout` | per-call timeout; default 120s |
| `-call-id` | explicit idempotency key |
| `-persist` | use PostgreSQL for `ModelCall`/`ModelCallAttempt` and audit |

Validation:

- `-call` requires exactly one of `-prompt` and `-prompt-file`.
- `-call` requires an endpoint and a provider model.
- `-program` requires `-persist`; without a store the resolver cannot read the
  program and must not guess.
- `-api-key-env` naming an unset/empty variable is an error.
- Without `-call`, all new flags are rejected so the plan output cannot silently
  pretend to make a call.

Call construction:

- Input message: one `user` message with the prompt.
- `Purpose`, `DataClass`, `MaxTokens`, `Budget`, `TaskID`, `Redacted`,
  `Escalation` come from the existing flags.
- The CLI always sets `ModelRequest.CallID` so it can report the id: `-call-id`
  wins; otherwise, with both `-task` and `-step`, it uses
  `StableCallID(task, step, purpose, PromptHash(req))`; otherwise it generates
  `call/cli/<RFC3339Nano>` locally. The router's auto-generated id is not used by
  this command because `ModelResponse` does not carry it.
- Router options: one OpenAI provider; the store-backed resolver; with
  `-persist`, `postgres.NewModelCallStore(pool)` and the PostgreSQL auditor,
  otherwise memory stores.

Output (stdout, JSON):

```json
{
  "call_id": "...",
  "provider": "local-openai",
  "model": "qwen3.8-flash-next-iq3_s",
  "content": "...",
  "usage": {"input_tokens": 63, "output_tokens": 32},
  "cost": {"amount": 0, "currency": "CNY"},
  "latency_ms": 1234
}
```

Errors go to stderr through the existing CLI error path and a non-zero exit.

## 8. Testing

### 8.1 Provider unit tests (httptest)

1. Success: request path `/v1/chat/completions`, `stream=false`, returns content
   and usage.
2. Base URL already ending in `/v1` is not doubled.
3. HTTP 500 → error, **not** `domain.ErrNotSent`.
4. Malformed JSON → error, not `ErrNotSent`.
5. Body over `MaxResponseBytes` → error.
6. Dial failure (closed port) → `errors.Is(err, domain.ErrNotSent)`.
7. Context deadline during response → error, not `ErrNotSent`.
8. Redirect response → error; no request to the redirect target.
9. Non-loopback endpoint → construction error.
10. Environment proxy variables are ignored (set `HTTP_PROXY` to a dead address in
    the test process and confirm the loopback call still succeeds).

### 8.2 Resolver tests

1. No ProgramID → `LOCAL_ONLY`, no escalation.
2. ProgramID with a nil store → error.
3. Stored `REMOTE_REDACTED_ONLY` is returned and `.Effective()` is applied.
4. Store error → error (router fails closed).
5. Request hints do not change the resolved policy.

### 8.3 CLI tests

1. Plan output is unchanged when `-call` is absent.
2. New flags without `-call` → error.
3. `-call` against an `httptest` server returns the expected JSON and records one
   attempt in a memory store.
4. Missing endpoint, missing model, both prompt flags, and `-program` without
   `-persist` → errors.
5. `SENSITIVE` data with a loopback endpoint succeeds; the same request with a
   non-loopback endpoint is rejected at provider construction.

### 8.4 Persistence

The existing Postgres integration tests already cover `ModelCallStore`. Add one
integration test that runs a `model -call -persist` flow against an httptest
server and asserts one `model_calls` row and one `model_call_attempts` row.

## 9. Documentation

- `architecture/model-audit.md`: add a section describing the loopback
  OpenAI-compatible provider, the endpoint normalization, the error
  classification, and `model -call`.
- `README.md` Implementation notes: one line for the real local provider.
- `CLAUDE.md` command list already contains `model`; no change needed.

## 10. Risks

- **Ambiguous retries**: only dial/TLS-before-send failures are retryable. A read
  timeout or 5xx stays `AMBIGUOUS`, which matches the existing router contract.
- **Proxy leakage**: explicitly disabled in the transport; a test pins it.
- **Prompt leakage**: errors and audit rows carry hashes only; nothing logs the
  prompt or response.
- **Concurrency**: the provider is stateless apart from its config; one instance
  is safe for concurrent `Complete` calls.

## 11. Acceptance

- `go test ./...`, `go vet ./...`, `CGO_ENABLED=0 go build ./...` pass.
- `bountygrid model` plan output is byte-identical to today for existing flags.
- `bountygrid model -call -endpoint http://127.0.0.1:8080/v1 -provider-model qwen3.8-flash-next-iq3_s -prompt "..."` returns content and usage and records an attempt.
- With `-persist`, the same call leaves one `model_calls` and one
  `model_call_attempts` row and one `model.call` audit event.
