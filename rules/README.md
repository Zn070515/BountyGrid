# Policy rules

Declarative, testable policy rules for `ACTIVE_HTTP` (and any other action).
Rules are data, not code: each rule selects a request, carries a decision and a
justification, and can include `match` / `not_match` examples that are validated
when the file is loaded.

This layer is intentionally small. It does not replace `internal/policy`
(scope, asset, eligibility, freshness, runtime constraints, hard DENY) and it
does not replace the `ExecutionGrant` (per-request method/host/path/RPS/
concurrency/daily limits). It only adjusts the approval/automation decision
after every domain gate has passed.

## Schema

```json
{
  "version": "2026-11-01",
  "rules": [
    {
      "id": "unique-rule-id",
      "actions": ["ACTIVE_HTTP"],
      "programs": ["vulbox/444"],
      "hosts": ["*.example.com"],
      "methods": ["GET", "HEAD"],
      "path_prefixes": ["/v1"],
      "decision": "allow",
      "justification": "why this rule exists",
      "match": [
        {"action": "ACTIVE_HTTP", "program": "vulbox/444", "asset": "https://api.example.com/v1/items", "method": "GET"}
      ],
      "not_match": [
        {"action": "ACTIVE_HTTP", "program": "vulbox/444", "asset": "https://example.com/v1/items", "method": "GET"}
      ]
    }
  ]
}
```

- `decision`: `allow`, `require_human`, or `forbidden`.
  `prompt`/`deny` are accepted as aliases and normalized.
- `source`: only `operator` (the default) is loadable from a file.
  - Operator guardrails may only tighten. An operator `allow` never raises a
    `REQUIRE_HUMAN` or `DENY`; it can only keep an existing `ALLOW`.
  - `source=platform_evidence` in a JSON file is **rejected**. Platform
    authority is not a hand-written rule: it is an Evidence-backed
    `authz.PermissionFact`, verified by `authz.BuildPlatformPolicy`, and only a
    verified `authz.PlatformPolicy` may relax a platform `CONDITIONAL` action.
    The `rules` package deliberately has no public platform-rule constructor.
    `rules/platform-evidence.example.json` is kept as a negative fixture; the
    loader must reject it.
- `actions` / `programs`: exact match.
- `hosts`: exact host, `*.example.com` (subdomains only, never the apex), or
  `**.example.com` (apex and subdomains). `*` alone is rejected. Ports, schemes
  and paths are rejected in host patterns.
- `methods`: uppercase HTTP methods.
- `path_prefixes`: segment-aware prefixes. `/api` matches `/api` and
  `/api/users`, never `/apix`, `/api2` or `/api-admin`. Paths are
  percent-decoded once, encoded separators/dot segments that survive decoding
  (e.g. `%252f`, `%2e`) are rejected, backslashes are treated as separators,
  and `path.Clean` removes dot segments.
- Selectors within one list are alternatives (OR); different selector fields
  are conjunctive (AND). Empty selector lists mean "any".
- `justification` is required for `require_human` and `forbidden`; it is shown
  in approval/rejection output and stored in `matched_rules`.
- `match` / `not_match` are load-time tests. A mismatch makes the whole rule
  file invalid; the control plane will not start with a broken policy.

## Evaluation semantics

- Operator rules and verified platform authority are evaluated separately, then
  combined by the policy engine. This separation is what stops an operator
  `allow` from impersonating platform authority.
- All operator rules are evaluated; **the strictest decision wins**
  (`forbidden > require_human > allow`).
- An operator `allow` never raises the ceiling: it can only keep an existing
  `ALLOW`. It can never loosen a hard `DENY` (`DoS`, internal network, lateral
  movement, credential stuffing) or a `CONDITIONAL` automation gate.
- A verified `authz.PlatformPolicy` permission may relax a platform
  `CONDITIONAL` action for an overridable action, but only when it is
  evidence-backed (Evidence `SourceType` `POLICY`/`ANNOUNCEMENT`, matching
  evidence content hash), names hosts and methods explicitly, has no
  restrictions, is fresh, is bound to the current `Program.PolicyRevision()`,
  and no operator rule tightened the decision. A same-platform program listing
  is not authorization. It still cannot override a hard `DENY`,
  `AUTOMATION_UNKNOWN`/`FORBIDDEN`, scope/eligibility/freshness or runtime
  constraints.
- `forbidden` always produces `DENY`.
- The effective decision records `RuleIDs` and `RuleSetFingerprint`. The
  fingerprint covers operator rules and platform authority provenance. The
  Action Gateway rejects `Authorize` if the active fingerprint differs from the
  one bound to the persisted decision.

## URL canonicalization

The matcher normalizes before comparing, so a rule cannot be bypassed by
cosmetic differences:

- host: lowercase, trailing dot removed, port stripped, IPv6 brackets removed;
- path: percent-decoded, forced to a leading `/`, `path.Clean`ed
  (`/v1/../admin` becomes `/admin`).

## CLI

```bash
go run ./cmd/bountygrid policy check \
  -rules rules/example.rules.json \
  -action ACTIVE_HTTP \
  -program vulbox/444 \
  -asset https://api.example.com/v1/items \
  -method GET
```

Output is JSON:

```json
{
  "rule_set_version": "2026-11-01-example",
  "rule_set_fingerprint": "sha256:...",
  "request": {"action": "ACTIVE_HTTP", "program": "vulbox/444", "asset": "https://api.example.com/v1/items", "method": "GET"},
  "decision": "allow",
  "can_relax": false,
  "matched_rules": [
    {"id": "vulbox-444-read-only", "source": "operator", "decision": "allow", "justification": "...", "matched_on": ["action=ACTIVE_HTTP", "program=vulbox/444", "host=*.example.com", "method=GET"]}
  ]
}
```

The command validates the rule file on load, including every `match`/`not_match`
example. It does not touch PostgreSQL. Because files are operator-only,
`can_relax` is always `false` for `policy check`; platform authority is never
loaded from a file.

## Platform authority (not a rule file)

Platform permissions exist in Go, not JSON:

```go
store := postgres.NewEvidenceStore(pool) // implements authz.EvidenceStore
contents, err := authz.NewFileContentReader(".evidence") // implements authz.EvidenceContentReader
if err != nil {
    return err
}
quote := "PERMISSION program=vulbox/444 action=ACTIVE_HTTP hosts=api.example.com methods=GET paths=/v1"
pp, err := authz.BuildPlatformPolicy(ctx, program, []authz.PermissionFact{{
    EvidenceID:          "ev-platform-policy-...",
    EvidenceContentHash: "sha256:...", // the hash of that Evidence record
    EvidenceQuote:       quote,        // must exactly match a PERMISSION line in the content
    Platform:            program.Platform,
    ProgramID:           program.ID,
    Action:              domain.ActionActiveHTTP,
    Hosts:               []string{"api.example.com"},
    Methods:             []string{"GET"},
    PathPrefixes:        []string{"/v1"},
    ObservedAt:          observedAt,
    ValidUntil:          validUntil,
    PolicyRevision:      program.PolicyRevision(),
    Justification:       "the platform policy page explicitly permits this endpoint",
}}, store, contents, now)
if err != nil {
    // reject: missing evidence, wrong platform/hash, non-authorizing source
    // type, missing quote/selectors, content hash mismatch, restrictions,
    // wrong revision, expired, stale
}

engine := policy.NewEngineWithRules(operatorRules).WithPlatformAuthority(pp)
```

`authz.VerifyPlatformEvidence` checks that the Evidence exists, belongs to the
same platform, matches `EvidenceContentHash`, has `SourceType` `POLICY` or
`ANNOUNCEMENT`, that `EvidenceQuote` exactly matches an explicit `PERMISSION`
line in the re-hashed evidence content, that every selector equals that line,
that the permission binds the program, names an action and non-empty
hosts/methods, has valid path prefixes, carries no `Restrictions`,
has a matching `policy_revision`, and is inside its observation window while
`Program.PolicyFreshAt` holds. Only a permission that passes all of them can
set `CanRelax`; `PlatformPolicy.Evaluate` re-runs the same checks at decision
time, so deleting or replacing the Evidence removes authority. A program-list
(`SourceType=PROGRAM`) Evidence is not authorization. See
[`architecture/authz.md`](../architecture/authz.md).

The CLI compiles the same facts from stored evidence instead of requiring a Go
caller:

```bash
go run ./cmd/bountygrid policy compile -program vulbox/444 -evidence ev-platform-policy-1 -content-root .
go run ./cmd/bountygrid decide -platform-evidence ev-platform-policy-1 -request req.json
```

`policy compile` reads the redacted evidence file through
`authz.FileContentReader`, parses only explicit `PERMISSION` lines, verifies
them with `authz.BuildPlatformPolicy`, and prints the resulting fingerprint.
Non-`POLICY`/`ANNOUNCEMENT` evidence is reported as skipped; unreadable or
malformed evidence fails the command. `decide -platform-evidence` is opt-in and
still cannot override hard `DENY`, operator guardrails, scope/eligibility/
freshness, or runtime constraints.

## Using rules in decisions

`bountygrid decide`, `bountygrid harness`, and `bountygrid submit` accept an
optional `-rules <file>` flag:

```bash
go run ./cmd/bountygrid decide -rules rules/example.rules.json -request req.json
```

These flags load **operator** rules only; passing a platform_evidence file is an
error. Without `-rules`, behavior is unchanged. The rule-set fingerprint is
persisted with the decision; a later `decide -authorize -rules <same-file>`
re-evaluates against the active rules and rejects a fingerprint mismatch.

## What this layer deliberately does not do

- It does not parse shell commands. BountyGrid's action is `ACTIVE_HTTP`, not a
  local shell; the equivalent of Codex's `execpolicy` is a host/method/path rule
  set, not a Starlark interpreter.
- It does not grant network access. The `ExecutionGrant` remains the per-request
  authority; a rule `allow` only removes an approval gate.
- It does not override scope, eligibility, freshness, automation `UNKNOWN`/
  `FORBIDDEN`, or runtime constraints. Those checks run first and fail closed.
