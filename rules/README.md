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
- `actions` / `programs`: exact match.
- `hosts`: exact host, `*.example.com` (subdomains only, never the apex), or
  `**.example.com` (apex and subdomains). `*` alone is rejected. Ports, schemes
  and paths are rejected in host patterns.
- `methods`: uppercase HTTP methods.
- `path_prefixes`: normalized path prefixes (percent-decoded, `path.Clean`ed).
- Selectors within one list are alternatives (OR); different selector fields
  are conjunctive (AND). Empty selector lists mean "any".
- `justification` is required for `require_human` and `forbidden`; it is shown
  in approval/rejection output and stored in `matched_rules`.
- `match` / `not_match` are load-time tests. A mismatch makes the whole rule
  file invalid; the control plane will not start with a broken policy.

## Evaluation semantics

- All rules are evaluated; **the strictest decision wins**
  (`forbidden > require_human > allow`).
- `allow` can loosen a project-conditional action only for actions explicitly
  marked overridable by the matrix (`ACTIVE_HTTP`, scanning, upload, etc.). It
  can never loosen a hard `DENY` (`DoS`, internal network, lateral movement,
  credential stuffing).
- `forbidden` always produces `DENY`.
- The effective decision records `RuleIDs` and `RuleSetFingerprint`. The Action
  Gateway rejects `Authorize` if the active rule set's fingerprint differs from
  the one bound to the persisted decision.

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
  "matched_rules": [
    {"id": "vulbox-444-read-only", "decision": "allow", "justification": "...", "matched_on": ["action=ACTIVE_HTTP", "program=vulbox/444", "host=*.example.com", "method=GET"]}
  ]
}
```

The command validates the rule file on load, including every `match`/`not_match`
example. It does not touch PostgreSQL.

## Using rules in decisions

`bountygrid decide`, `bountygrid harness`, and `bountygrid submit` accept an
optional `-rules <file>` flag:

```bash
go run ./cmd/bountygrid decide -rules rules/example.rules.json -request req.json
```

Without `-rules`, behavior is unchanged. The rule-set fingerprint is persisted
with the decision; a later `decide -authorize -rules <same-file>` re-evaluates
against the active rules and rejects a fingerprint mismatch.

## What this layer deliberately does not do

- It does not parse shell commands. BountyGrid's action is `ACTIVE_HTTP`, not a
  local shell; the equivalent of Codex's `execpolicy` is a host/method/path rule
  set, not a Starlark interpreter.
- It does not grant network access. The `ExecutionGrant` remains the per-request
  authority; a rule `allow` only removes an approval gate.
- It does not override scope, eligibility, freshness, automation `UNKNOWN`/
  `FORBIDDEN`, or runtime constraints. Those checks run first and fail closed.
