# CLAUDE.md

## Project overview

BountyGrid is a Go control plane for three Chinese bug bounty platforms: Alibaba Xianzhi/ASRC, Vulbox, and Butian. It is **not** a scanner and does not perform vulnerability testing. It turns platform facts, account eligibility, program scope, testing constraints, competition data, and revenue outcomes into auditable decisions that a later harness/model layer can execute only after policy approval.

The repository has three lines:

- `platform-audit/` — the factual baseline: account state, programs, scope visibility, rules, AI policy, payout, automation boundaries.
- `architecture/` — the full Go design: domain model, evidence/fact/state pipeline, adapters, policy engine, opportunity/scheduler, harness/model router, verification/submission/ledger.
- `internal/` — implementation. Phase 0 (domain + evidence + PostgreSQL repositories/rollback) is complete. The core kernel (audit + facts + state + policy, canonical Phase 2/3) is implemented early and hardened; the canonical Phase 1 Browser Sidecar/Readers are NOT started.

Read [`README.md`](README.md) first, then [`architecture/README.md`](architecture/README.md), [`architecture/implementation-order.md`](architecture/implementation-order.md), and [`platform-audit/README.md`](platform-audit/README.md). Do not treat a page observation as a domain rule; findings flow through `Evidence → ObservedFact → DerivedState → PolicyDecision`.

## Development environment

Use Go **1.27.3**. On the current Windows machine the base install is 1.25.5 under `D:\Program Files` (no admin write); the shell uses the official toolchain switch:

```bash
go env -w GOTOOLCHAIN=go1.27.3
go version   # go1.27.3 windows/amd64
```

PostgreSQL is a single-machine Docker container:

```text
container: bountygrid-pg
image:     postgres:16-alpine
port:      127.0.0.1:55434 -> 5432
database:  bountygrid
user:      bountygrid
dsn:       .env.local (gitignored; never commit or print the password)
```

First setup and verification:

```bash
docker start bountygrid-pg
cp env.example .env.local   # fill the local password, or keep an existing .env.local
go run ./cmd/bountygrid migrate
go run ./cmd/bountygrid ping
go test ./...
go vet ./...
go build ./...
```

`cmd/bountygrid` supports `migrate`, `ping`, and `version`. Migrations are embedded SQL under `internal/store/postgres/migrations/`; add a new numbered file for schema changes, never edit an applied migration.

The read-only probes under `probes/` are **not** product code and are not part of the Go module. They use agent-browser sessions with per-platform `--state` files. Probes may read pages, rules, dates, counts, and account status; they must not sign up, accept NDA, take compliance exams, claim tasks, submit reports, or withdraw.

## Runtime operations

Development is single-machine, single-user, single database/schema. Do not introduce multi-tenant assumptions or horizontal scaling without an ADR. Keep the connection pool small (currently `MaxConns=4`). The application loads `.env.local` only when `DATABASE_URL` is not already set in the environment.

Evidence raw files and screenshots stay local. The database stores metadata, hashes, and references; it does not store passwords, cookies, ID numbers, bank numbers, or full phone/email values. Browser session state stays under the agent-browser state directory, never in the repository.

## Engineering rules

- **No state without evidence.** Every `ObservedFact` needs `evidence_refs`, `observed_at`, `confidence`, and `extractor_version`; every `DerivedState` needs inputs and `valid_until`.
- **Eligibility is `Decision + Gates[]`.** Never collapse it to a single enum. Account-level gates (`COMPLIANCE_EXAM`, `PRACTICAL_CERTIFICATION`, `BANK_BINDING`) live in `AccountCapability`, not `Program.Gates`.
- **Scope must not degrade.** `Visibility` is disclosure; `Access` is the current account's access. If `Access != GRANTED`, `ContentsKnown` is false and include/exclude/conditional lists are empty. A signed-up account may be `GATED_BY_SIGNUP + GRANTED`; never rewrite disclosure to PUBLIC to mean "unlocked".
- **Policy is action-level.** Evaluate `Actor + Account + Program + Asset + Action + Time`. `VIEW_RULES`, `READ_SCOPE`, `ACTIVE_HTTP`, `REPORT_SUBMIT`, and `PAYOUT` have different decisions. Eligibility is only evaluated for participation actions; viewing rules/program is not gated by eligibility.
- **Execution requires an asset.** `ACTIVE_HTTP` and similar actions need an explicit `AssetID` that matches the program and `Scope.CheckAsset` (INCLUDED / CONDITIONAL / EXCLUDED / UNKNOWN).
- **Automation policy is authoritative.** `AutomationPolicy=FORBIDDEN` cannot be overridden by human approval; `ALLOW` may override the conservative default matrix only for project-explicit actions.
- **Human-only actions.** `REPORT_SUBMIT`, `PROGRAM_SIGNUP`, `NDA_ACCEPT`, `COMPLIANCE_EXAM`, `PRACTICAL_CERTIFICATION`, `PAYOUT`, and `BANK_BINDING` never receive `ALLOW` without a human approval. `REPORT_SUBMIT` approval is single-report, single-use, bound to `request_id/asset/resource/parameters_hash`, and consumed by an `ApprovalLedger`.
- **Dates are business dates.** Date-only platform values are parsed in `Asia/Shanghai`; end dates are inclusive (`23:59:59.999999999`), and the fact unit is `date`, never UTC midnight.
- **Internal endpoints are observed transports.** Xianzhi `openapi/cst/*`, Vulbox `vapi.vulbox.com/*`, and Butian AJAX endpoints are `BROWSER_OBSERVED_API`, not official APIs. Do not bypass authentication or call admin APIs.
- **No real testing in this repository.** Do not generate payloads, run scanners, brute force, enumerate directories/subdomains, pivot into internal networks, or attempt DoS. The harness may only execute actions with an unexpired `ALLOW` decision.
- **Secrets never enter git.** `.env.local`, screenshots (`*.png/*.jpg`), browser state, cookies, tokens, ID/bank/phone/email values are excluded. Keep it that way.
- **Update the contract when the domain changes.** Changing a domain type requires updating its invariants/tests, and changing policy/scope semantics requires updating `architecture/` and `platform-audit/` where relevant.
- **Keep reviewer notes out of the root.** Put long review/advice documents under `docs/advices/`, not as `ChatGPT.md` or similar root dumps.
- **Docs are part of the product.** If behavior changes, update the matching README/architecture section; do not leave a stale claim that an agent will later treat as fact.

## Git workflow

Work on a short-lived branch from the clean, updated `main` (`feat/*`, `fix/*`, `docs/*`, or `probe/*`). Run the focused gate before committing:

```bash
go test ./...
go vet ./...
go build ./...
```

For database changes, also run `go run ./cmd/bountygrid migrate` against the local container. For probe changes, run the probe read-only and report its JSON output; do not mutate platform state.

Merge the verified branch back into `main` with `--no-ff` and push. Do not rewrite or force-push shared history. Do not commit generated binaries, `.env.local`, screenshots, or probe output.

Commit attribution follows the repository convention; never misattribute authorship or reviewer identity.
