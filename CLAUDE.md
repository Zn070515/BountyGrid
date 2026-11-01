# CLAUDE.md

## Project overview

BountyGrid is a Go control plane for Alibaba Xianzhi/ASRC, Vulbox, and Butian. It is **not** a scanner and does not perform vulnerability testing. It turns platform facts, eligibility, scope, constraints, competition, and revenue into auditable decisions; a harness may execute only after policy approval.

- `platform-audit/` — the factual baseline.
- `architecture/` — the design (domain, evidence/fact/state, adapters, policy, opportunity, harness, model, verification, submission, ledger, trace).
- `internal/` — implementation. Control-plane phases 1–8 are implemented; a real platform SubmitAdapter and real model provider are absent. **Active execution remains disabled**: no `ACTIVE_HTTP` tool exists.

Read [`README.md`](README.md), [`architecture/README.md`](architecture/README.md), [`architecture/implementation-order.md`](architecture/implementation-order.md), and [`platform-audit/README.md`](platform-audit/README.md) first. A page observation is not a domain rule; use `Evidence → ObservedFact → DerivedState → PolicyDecision`.

## Development environment

Go **1.27.3** (the machine's base install is 1.25.5; use the toolchain switch):

```bash
go env -w GOTOOLCHAIN=go1.27.3
go version   # go1.27.3 windows/amd64
```

PostgreSQL is the local Docker container `bountygrid-pg` (`postgres:16-alpine`, `127.0.0.1:55434`, database/user `bountygrid`). The DSN is in `.env.local` (gitignored; never print the password).

```bash
docker start bountygrid-pg
cp env.example .env.local   # or keep an existing .env.local
go run ./cmd/bountygrid migrate
go run ./cmd/bountygrid ping
go test ./... && go vet ./... && go build ./...
```

`cmd/bountygrid` supports `migrate`, `rollback`, `ping`, `sync`, `derive`, `decide`, `schedule`, `competition`, `harness`, `model`, `policy`, `trace`, `verify`, `report`, `submit`, `ledger`, `version`. Migrations are embedded SQL under `internal/store/postgres/migrations/`; add a new numbered file, never edit an applied migration.

`sync -platform vulbox|xianzhi|butian` is the read-only Reader (`-programs`, `-account`, `-program <id>`, `-persist`; Butian also `-public-welfare`/`-grow`). It uses the `agent-browser` sidecar and the existing `vbprobe`/`xzprobe`/`btprobe` session; keep the session alive (`-close=false`). It never signs up, accepts NDA/rules, takes exams, or submits. Raw pages stay in `.evidence/` (gitignored). Probes under `probes/` are not product code.

## Runtime boundaries

Single-machine, single-user, single database/schema; keep the pool small (`MaxConns=4`). Do not add multi-tenancy or horizontal scaling without an ADR. The database stores metadata, hashes, and references — never passwords, cookies, ID/bank numbers, or full phone/email values. Browser state stays outside the repository.

## Engineering rules

- **Evidence first.** Every `ObservedFact` needs `evidence_refs`, `observed_at`, `confidence`, `extractor_version`; every `DerivedState` needs inputs and `valid_until`. A superseded Fact expires dependent states. A listing is not a platform status; a registration window is not proof this account is unregistered.
- **Eligibility and scope.** `Eligibility = Decision + Gates[]`; account gates live in `AccountCapability`, not `Program.Gates`. `Visibility` is disclosure, `Access` is this account's access; a non-granted scope has no contents. Login is not eligibility.
- **Policy and authorization.** Decide per `Actor + Account + Program + Asset + Action + Time`. `VIEW_RULES`, `READ_SCOPE`, `ACTIVE_HTTP`, `REPORT_SUBMIT`, and `PAYOUT` differ. Execution needs an authoritative asset from `Program.Scope.ResolveAsset`, fresh `Program.Status`/`Scope`/`PolicyObservedAt`/`AccountCapability`, and `Authorize` re-evaluates before consuming a single-use approval. Hard `DENY` cannot be approved away.
- **Rules are a guardrail, not authority.** `internal/policy/rules` overlays the matrix after every domain gate. Operator rules may only tighten; only unexpired `platform_evidence` rules with evidence/freshness/policy revision may relax a `CONDITIONAL` action. Details: [`rules/README.md`](rules/README.md).
- **Verification proves execution.** `VERIFIED` requires scope, reproduction/impact evidence, data minimization, duplicate check, a `COMPLETED` `ExecutionReceipt` matching program/asset/action/decision/evidence, and a durable `HumanReview` for AI-assisted work. A model cannot produce `VERIFIED`; missing evidence/receipts/review fail closed.
- **Report and submission.** `report.Build` only assembles `DRAFT` from `VERIFIED`; platform-specific fields and AI disclosure apply. `submit` re-checks the stored draft and current Scope, binds the approval to `report_id + report_content_hash`, and calls the Submitter only after `ALLOW` + `AuthorizeWith`. Only `DryRunSubmitter` exists; submission intents never auto-retry after `SENT`/`AMBIGUOUS`.
- **Ledger is append-only cash.** CNY only; `PAID` is net cash; `FEE`/`TAX` are informational; `REVERSAL` only corrects `PAID`/`REFUND`; `ExternalRef` prevents duplicate imports; `Reconcile` reports, never edits. Non-cash rewards use `NonCashRecorder`.
- **Tools and network.** File tools get only `harness.Workspace` (os.Root); network tools get only a grant-bound `Egress`. The proxy is the per-request boundary and must be a `GrantPolicy` with an audit sink in production; opaque CONNECT is opt-in. Egress rejects non-public IPs and never uses environment proxies. Details: [`architecture/proxy.md`](architecture/proxy.md), [`architecture/harness-model-router.md`](architecture/harness-model-router.md).
- **Model output is not an action.** The Model Router enforces data class/provenance, locality, context/capability and cumulative budget, and persists `model_calls` with `RESERVED/STARTED/COMPLETED/AMBIGUOUS/FAILED_BEFORE_SEND`. Only `FAILED_BEFORE_SEND` is retryable. Model responses cannot create decisions or platform actions.
- **Trace is not truth.** `internal/trace` is append-only JSONL plus content-addressed raw payloads; `SafeSink` enforces schema, redaction, data class and parent binding. Database state remains authoritative. Details: [`architecture/trace.md`](architecture/trace.md).
- **Bulk DB and concurrency.** Use `pgx.Batch` (not `CopyFrom`) for idempotent bulk writes; bounded worker pools; platform Readers stay sequential. Do not add unbounded goroutines or parallel browser reads.
- **Time, logging, CI.** Business dates use `internal/timeutil` (Asia/Shanghai, inclusive end dates); CLI uses `signal.NotifyContext`; logs use `log/slog` on stderr. CI gate: `go test -race -shuffle=on -count=1 ./...`, benchmark smoke, `go vet`, `CGO_ENABLED=0 go build`, `govulncheck`.
- **No real testing.** Do not generate payloads, scan, brute force, enumerate, pivot into internal networks, or attempt DoS. A harness may only execute an unexpired `ALLOW`.
- **Secrets and docs.** Never commit `.env.local`, screenshots, browser state, cookies, tokens, or ID/bank/phone/email values. Keep reviewer notes under `docs/advices/` (gitignored, local-only). If behavior changes, update the matching `architecture/` or `platform-audit/` page; do not add every implementation detail to `README.md`.

## Git workflow

Branch from a clean, updated `main` (`feat/*`, `fix/*`, `docs/*`, `probe/*`). Before committing run `go test ./...`, `go vet ./...`, `go build ./...`; for schema changes also run `bountygrid migrate`. Merge with `--no-ff`; do not rewrite shared history. Do not commit binaries, `.env.local`, screenshots, or probe output. Keep commit attribution accurate.
