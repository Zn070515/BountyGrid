# Trace bundle

`internal/trace` is an append-only, content-addressed event log for one research
run. It complements PostgreSQL: the database is the authoritative current state,
while the trace is the replayable causal history of how that state was reached.

It is deliberately separate from `audit_events`: audit answers "what did the
control plane decide", a trace answers "which model call, decision, tool
dispatch, receipt and evidence produced this outcome, in what order, and from
which cause".

## Layout

```text
<run>/
  trace.jsonl        one Event per line, append-only, 0600
  raw/<sha256>       raw redacted payloads, content-addressed, 0600
```

## Event

```json
{
  "id": "evt-...",
  "trace_id": "trace-...",
  "parent_id": "evt-...",
  "seq": 17,
  "kind": "TOOL_DISPATCH",
  "data_class": "INTERNAL",
  "entity_type": "receipt",
  "entity_id": "rcpt/...",
  "actor_kind": "HARNESS",
  "actor_id": "harness-1",
  "at": "2026-11-01T12:00:00Z",
  "payload": {"tool": "active_http"},
  "hash": "sha256:..."
}
```

Kinds: `MODEL_CALL`, `ACTION_DECISION`, `TOOL_DISPATCH`, `EXECUTION_RECEIPT`,
`EVIDENCE`, `FINDING`, `VERIFICATION`, `REPORT`, `SUBMISSION`, `LEDGER`,
`CUSTOM`.

`Seq` is assigned by the writer under one mutex; it is monotonic within a file.
`ParentID` links an event to its cause. `Hash` covers the key fields plus
canonicalized payload (UTC time, sorted keys) and is stable across a JSON
round-trip. `DataClass` is optional (`PUBLIC`, `INTERNAL`, `SENSITIVE`) and is
validated when non-empty; when present it is also covered by the hash, while an
empty value leaves hashes of existing events unchanged.

## Writer guarantees

- Append-only JSONL; existing lines are never rewritten or truncated.
- One event per line; malformed existing files are rejected on `Open`.
- 0600 file mode; `Close` syncs and reports flush errors.
- Concurrent `Append` calls are serialized; `Seq` cannot duplicate within one
  process.
- No cross-process lock: two processes must not append to the same trace file.
- Appends are not fsynced individually; a crash may lose the tail after the
  last successful `Close`.

## Raw payloads

`RawStore.Put` writes `raw/<sha256>` via a 0600 temp file, `Sync`, and rename;
identical content is deduplicated. `Get` re-hashes the bytes and fails closed on
a content mismatch. Refs must be `sha256:<64 hex>`; other forms (including path
traversal) are rejected.

## Safe sink

`SafeSink` is the enforced entry point and should be used instead of writing to
`Writer`/`RawStore` directly. It is the single point where schema, redaction,
hash and parent binding are applied:

- `Append` validates the event, optionally requires `DataClass`
  (`RequireDataClass`), refuses a `ParentID` this sink has not already appended
  (`AllowDanglingParents` opts out), scans `Payload` with `redact.Scan`, then
  writes and records the event ID for later parent checks.
- `PutRaw` scans the bytes with `redact.Scan` before storing them, so a returned
  content reference always points at scanned data.
- `Resume(path)` seeds the parent set from an existing trace, so a restarted
  process can append children of events written before the restart.
- Rejections name only the finding kind, never the matched value.

Direct `Writer`/`RawStore` callers remain responsible for redaction: "what may
leave the process" is still a caller decision (`internal/redact` /
`internal/audit`), and the raw `Writer` API does not re-check it.

## Reducer

`Reduce` sorts by `Seq` then `At`, builds the parent/child tree, and rejects:

- multiple `TraceID`s in one bundle;
- empty or duplicate event IDs;
- parent cycles.

An unknown `ParentID` becomes a root. That keeps a truncated or externally
merged trace readable; structural corruption (duplicate IDs, cycles) still
fails closed. `ByKind` counts events per kind.

## CLI

```bash
bountygrid trace reduce -file <trace.jsonl>
bountygrid trace validate -file <trace.jsonl>
```

`reduce` prints a nested root/children summary; `validate` prints the trace ID,
event count, root count and per-kind counts. Neither command writes to
PostgreSQL or the network.

## Current integration status

The package and CLI are implemented and tested. Model calls, policy decisions,
tool dispatches, receipts, evidence, verification, report, submission and ledger
call sites do **not** emit trace events yet. Wiring is intentionally a separate
step: the trace must never become a second source of truth, and every write must
carry already-redacted data.

## Deliberate limits (v1)

- No cross-process writer lock.
- `Read` caps one JSONL line at 16 MiB.
- `validate` does not recompute every stored `Hash`; hash stability is covered by
  tests, not yet by the CLI.
- No automatic retention/compaction; a trace is scoped to one run and can be
  deleted/archived by the operator.
