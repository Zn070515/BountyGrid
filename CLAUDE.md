# CLAUDE.md

## Project overview

BountyGrid is a Go control plane for three Chinese bug bounty platforms: Alibaba Xianzhi/ASRC, Vulbox, and Butian. It is **not** a scanner and does not perform vulnerability testing. It turns platform facts, account eligibility, program scope, testing constraints, competition data, and revenue outcomes into auditable decisions that a later harness/model layer can execute only after policy approval.

The repository has three lines:

- `platform-audit/` — the factual baseline: account state, programs, scope visibility, rules, AI policy, payout, automation boundaries.
- `architecture/` — the full Go design: domain model, evidence/fact/state pipeline, adapters, policy engine, opportunity/scheduler, harness/model router, verification/submission/ledger.
- `internal/` — implementation. Phase 0 is complete. Phase 1 Readers (Browser Sidecar + Vulbox/Xianzhi/Butian) are implemented and have been exercised against the live accounts. The canonical Phase 2/3 kernel (facts/state/policy) and Phase 4/5 surfaces (Opportunity/Scheduler/CompetitionSnapshot, Harness registry/gateway, Model Router routing/budget/audit) are implemented, but Phase 1–5 are only partially complete as a whole. The Model Router has no real provider adapter yet; `bountygrid model` only prints a routing plan. **Active execution remains disabled**: no ACTIVE_HTTP tool exists in this repository and the built-in tools are local read-only.

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

`cmd/bountygrid` supports `migrate`, `rollback`, `ping`, `sync`, `derive`, `decide`, `schedule`, `competition`, `harness`, and `version`. Migrations are embedded SQL under `internal/store/postgres/migrations/`; add a new numbered file for schema changes, never edit an applied migration.

`sync` is the Phase 1 read-only Reader entry point (`-platform vulbox|xianzhi|butian`). It uses the `agent-browser` sidecar (`internal/sidecar`) and the existing `vbprobe`/`xzprobe`/`btprobe` session on this machine; pass `-session` on other environments. `-programs` reads the public list, `-account` reads the logged-in account, and `-program <native-id>` reads one program's Scope/Policy. Butian additionally supports `-public-welfare` (`/Reward/pub?p=N`, paginated via `-pages`) and `-grow` (rules modal + activity center); both are opt-in optional reader capabilities (`adapter.PublicWelfareReader` / `adapter.GrowReader`) so absence is never reported as an empty list. `-persist` writes Evidence → Facts → Program/Account to PostgreSQL (Evidence first; Facter carry EvidenceRef); raw pages stay in `.evidence/` (gitignored). A list page that explicitly shows enrollment (`IsJoin`, “已报名”, “提交漏洞” after signup) also feeds `Account.JoinedProgramIDs` so policy can resolve `PROGRAM_SIGNUP` gates at decision time. It never fills, signs up, accepts NDA, or submits; the only scripted clicks are pagination controls, rule modals, and dismissing read-only promotional overlays (never a signup/NDA/submit action). Keep the session alive (`-close=false` is the default) so the manual login is not discarded.

The read-only probes under `probes/` are **not** product code and are not part of the Go module. They use agent-browser sessions with per-platform `--state` files. Probes may read pages, rules, dates, counts, and account status; they must not sign up, accept NDA, take compliance exams, claim tasks, submit reports, or withdraw.

## Runtime operations

Development is single-machine, single-user, single database/schema. Do not introduce multi-tenant assumptions or horizontal scaling without an ADR. Keep the connection pool small (currently `MaxConns=4`). The application loads `.env.local` only when `DATABASE_URL` is not already set in the environment.

Evidence raw files and screenshots stay local. The database stores metadata, hashes, and references; it does not store passwords, cookies, ID numbers, bank numbers, or full phone/email values. Browser session state stays under the agent-browser state directory, never in the repository.

## Engineering rules

- **No state without evidence.** Every `ObservedFact` needs `evidence_refs`, `observed_at`, `confidence`, and `extractor_version`; every `DerivedState` needs inputs and `valid_until`. States are recomputed by `bountygrid derive`; a superseded Fact expires every state whose `inputs` contains it. An explicit `RUNNING` fact must not override a passed inclusive end date.
- **A listing is not a status.** Butian public-welfare membership emits `butian.pub.listed=true`; RUNNING is a derived state at 0.8 confidence, not an observed platform status. Program gates record requirements; `PRACTICAL_CERTIFICATION` and `PROGRAM_SIGNUP` satisfaction is resolved against the account at policy time, never frozen as `UNSATISFIED` by a Reader. "There is a registration window" is not "this account has not registered".
- **The sidecar is a transport, not a sandbox.** `agent-browser` `Eval` is mutation-capable. Readers confine it to read-only observations and pagination/modal controls; Harness/Model code must never receive a `sidecar.Client`.
- **Eligibility is `Decision + Gates[]`.** Never collapse it to a single enum. Account-level gates (`COMPLIANCE_EXAM`, `PRACTICAL_CERTIFICATION`, `BANK_BINDING`) live in `AccountCapability`, not `Program.Gates`. A gate only invalidates the capability it governs: `BANK_BINDING`/`PAYOUT_SETUP` block payout, not submission; `COMPLIANCE_EXAM`/`IDENTITY_VERIFIED` block submission.
- **Login is not eligibility.** A logged-in browser session does not mean the account is signed up, compliant, practically certified, or authorized to see a GATED scope. Xianzhi asset scope requires actual signup; Butian submission requires the quarterly compliance exam (and practical certification for exclusive SRC); keep those as `GATED_BY_SIGNUP` / account gates, never infer access from "已登录".
- **Scope must not degrade.** `Visibility` is disclosure; `Access` is the current account's access. If `Access != GRANTED`, `ContentsKnown` is false and include/exclude/conditional lists are empty. A signed-up account may be `GATED_BY_SIGNUP + GRANTED`; never rewrite disclosure to PUBLIC to mean "unlocked".
- **Policy is action-level.** Evaluate `Actor + Account + Program + Asset + Action + Time`. `VIEW_RULES`, `READ_SCOPE`, `ACTIVE_HTTP`, `REPORT_SUBMIT`, and `PAYOUT` have different decisions. Eligibility is only evaluated for participation actions; viewing rules/program is not gated by eligibility.
- **Model output is not an action.** The Model Router (`internal/model`) selects a provider by purpose/data class, enforces output-token and budget caps, and audits `model.call` with hashes/usage/cost only. `SENSITIVE` data never routes to a non-local provider; no eligible provider is a hard error, never a silent remote fallback. Model responses cannot create an `ActionDecision` or platform action. Real provider adapters must read credentials from the environment, never from git; the current `StaticProvider` is local and network-free.
- **Decisions are recorded, then re-checked.** `bountygrid decide` persists the `ActionDecision` and an `action.decision` audit event; `decide -authorize` must pass before execution (ALLOW, unexpired, program RUNNING/fresh for execution actions). A DENY never produces a platform action. Decision IDs use separated timestamps, not long digit runs, so audit payloads survive the secret scanner.
- **Never rank by advertised maximum bounty.** Opportunity scores use expected return (P(valid), min/median/max, duplicate/competition penalty) divided by cost times confidence, and record explainable inputs. The scheduler never dispatches PAUSED/ENDED programs; a human queue (`HUMAN_ACTION_REQUIRED`) is not execution, and only `QUEUED` research tasks may later be handed to a harness after an Action Gateway ALLOW.
- **Competition snapshots need evidence.** `bountygrid competition` imports a snapshot with `evidence_url/evidence_text`, redacts the text locally, writes Evidence and `competition_snapshots`. Missing top10 share uses a conservative 0.5 and lower confidence, never a silent high-confidence default.
- **Tools cannot bypass Policy or the sandbox.** A harness tool declares `RequiredAction`; the harness overrides the request action, calls the Action Gateway, requires `ALLOW` + `Authorize`, then applies the budget and egress sandbox before executing. DENY/REQUIRE_HUMAN must not call the tool. Network tools receive only the harness `Egress` client, which re-checks every request and redirect against `Sandbox.AllowedHosts`; a network tool with an empty allowlist is rejected. Side-effect tools (`NetworkEgress`/`WritesFiles`/`Destructive`) require an `IdempotencyKey`; the key is reserved before execution and a retry replays the `ExecutionReceipt` instead of running again. Built-in tools are read-only; there are no scan/brute-force/DoS/submit tools in this repository.
- **Execution requires an authoritative asset.** `ACTIVE_HTTP` and similar actions need an explicit `AssetID`; the Action Gateway resolves the canonical `Asset` from `Program.Scope.ResolveAsset` and rejects an ID that is not stored in the scope (`ASSET_UNKNOWN`). Callers cannot supply an `Asset` value. `Scope.CheckAsset` must return INCLUDED / CONDITIONAL / EXCLUDED / UNKNOWN.
- **Policy inputs must be self-consistent.** `DecisionContext.Account.ID/Program.ID/Platform` must match the `ActionRequest`; mismatches are `DENY`. `ALLOW` decisions must carry `ExpiresAt`, and execution requires `Program.Status.Confidence >= 0.8`.
- **Authorize re-evaluates, then consumes approval.** The persisted decision carries its `ActionRequest` and runtime inputs. `Authorize` reloads Account/Program, re-resolves the asset from Scope, and re-runs Policy; any change after the decision (scope, automation, eligibility, status) turns the old `ALLOW` into an error. A single-use `HumanApproval` is consumed atomically only after this revalidation succeeds, never inside `Policy.Decide`.
- **Constraints are runtime-enforced.** Time windows, `MaxRPS`, `MaxConcurrency`, VPN, dedicated egress IP, researcher headers, test-account attestation and allowed/forbidden HTTP methods are checked against `ExecutionEnvironment`; missing runtime proof is `REQUIRE_MORE_EVIDENCE`, an observed violation is `DENY`. An environment older than `ExecutionEnvironmentMaxAge` (5 minutes) is treated as unknown. Daily limits/budget caps with no supplied usage/budget are also unknown, never "zero used". `AuthorizeWith` must receive fresh usage/budget observations; `Authorize` without context fails closed for constrained programs.
- **Decisions and audit share a transaction.** PostgreSQL `RecordDecisionWithAudit` writes the decision and `action.decision` event in one transaction; the gateway prefers it over two independent writes.
- **Facts and derived state decay.** A `DerivedState` cannot outlive its inputs: `ValidUntil` is capped by the oldest selected Fact's `observed_at + DefaultFactTTL` (30 days), and facts older than that cannot produce a state. `CompetitionSnapshot` has a `ValidUntil`; snapshots older than 7 days lose confidence and those older than 30 days are ignored. Shares must satisfy `top1 <= top3 <= top10`.
- **Research and opportunity history are append-only.** `ResearchTask` IDs include a schedule-run ID (`task/<program>/<run>`), `Opportunity` IDs include the evaluation time, and PostgreSQL keeps at most one `QUEUED` task per program while superseding older queued entries. The scheduler reads persisted task times for its cooldown.
- **Scope matching is fail-closed.** `ContentsKnown=true` with an empty `Include` means no executable assets, not "everything"; `*.example.com` matches subdomains only, never the apex.
- **Automation policy is authoritative.** `AutomationPolicy=FORBIDDEN` cannot be overridden by human approval; `ALLOW` may override the conservative default matrix only for project-explicit actions. `CONDITIONAL` may add a human gate but must never turn a hard `DENY` into an approvable action (no approval for DoS/internal-network/lateral-movement).
- **Decisions expire with their inputs.** `ALLOW.ExpiresAt` is the earliest of the cache window, `Program.Status.ValidUntil`, and the approval expiry. Ledger infrastructure errors surface as errors; only `ErrApprovalConsumed` maps to `REQUIRE_HUMAN`.
- **Human-only actions.** `REPORT_SUBMIT`, `PROGRAM_SIGNUP`, `NDA_ACCEPT`, `COMPLIANCE_EXAM`, `PRACTICAL_CERTIFICATION`, `PAYOUT`, and `BANK_BINDING` never receive `ALLOW` without a human approval. `REPORT_SUBMIT` approval is single-report, single-use, bound to `request_id/asset/resource/parameters_hash`, and consumed by an `ApprovalLedger`; the PostgreSQL implementation uses `consumed_approvals` so the consumption survives restarts.
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
