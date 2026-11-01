# Capability plane: limits, leases and revocation

`internal/capability` is the shared execution-capability plane. `Egress` and
the local proxy (and any later execution surface) acquire from the **same**
broker and validate the **same** grant type. The package has no policy
authority: it cannot turn a Deny into an Allow, and it never creates a decision.

```text
ActionDecision (ALLOW)
   │  buildGrant: scope/policy revisions, 60s lease, epoch
   ▼
ExecutionGrant ──► Egress ─┐
   │                       ├──► capability.Broker.Acquire
   └── GrantPolicy ──► Proxy┘        (RPS, concurrency, daily quota)
                                    │
GrantPolicy.Validator ◄─────────────┘  ProgramValidator re-reads Program
                                       + revocation epoch before I/O
```

## Broker and leases

```go
type Request struct {
    AccountID      domain.AccountID
    ProgramID      domain.ProgramID
    Interface      string   // "http", "https_connect", ...
    MaxRPS         *float64
    MaxConcurrency *int
    DailyLimit     int64
    Now            time.Time
}

type Broker interface {
    Acquire(ctx context.Context, req Request) (Lease, error)
}

type Lease interface{ Release() }
```

`NewMemoryBroker` is the default and owns one `programLimiter` per
`account + program + interface` key (see `QuotaKey`). It enforces:

- **RPS**: one shared schedule (`last` slot). The strictest positive rate ever
  observed wins; a later lower rate tightens the schedule instead of resetting
  it. Waiting is context-aware; two grants for the same key do not each get a
  fresh schedule.
- **Concurrency**: one shared in-flight counter. The strictest observed ceiling
  wins, releases close/replace a notify channel so blocked acquirers re-check
  without polling, and `Release` is idempotent.
- **Daily quota**: delegated to `DailyQuota` (see below). `limit == 0` has no
  ceiling but still counts, so a later tighter grant sees the true usage.

Acquisition order is concurrency slot → RPS wait → daily consume. A request
cancelled while waiting for concurrency or RPS never spends a daily slot, and a
daily-quota rejection releases the concurrency slot it held. A request with
neither account nor program identity receives an ephemeral, non-shared limiter:
an unattributable grant must not reset or block another program's budget.

`DurableBroker` is a `Broker` whose daily counter survives a restart; the
production proxy requires one (`Durable()` reports whether the injected
`DailyQuota` is durable). `MemoryBroker` is only as durable as its quota.

`ErrLimit` wraps `ErrGrantDenied`, so existing callers that match the grant
sentinel keep failing closed while new callers can map the daily limit to HTTP
429.

### Wiring

```go
broker := capability.NewMemoryBroker()                 // process-local
h.SetBroker(broker)                                    // Egress
p := harness.NewProxy(harness.ProxyConfig{
    Policy: harness.GrantPolicy{Grant: grant, Validator: validator},
    Broker: broker,                                    // same broker
    Audit:  sink,
})
```

`Egress.WithBroker` returns a copy bound to a shared broker. `Harness.SetBroker`
replaces the base Egress broker. Neither widens a grant.

## Durable daily quota

```go
type DailyQuota interface {
    Consume(ctx context.Context, key string, businessDate string, limit int64) (bool, error)
}
```

- `MemoryDailyQuota` is the default (tests, single process).
- `PostgresDailyQuota{Store: repo}` persists counts in `network_usage`
  (`account_id, program_id, interface, business_date, count`). Inject it with
  `NewMemoryBroker(WithDailyQuota(...))`.

`Repo.ConsumeNetworkUsage` performs the check and increment in one statement:

```sql
INSERT ... VALUES (..., 1, now())
ON CONFLICT (account_id, program_id, interface, business_date)
DO UPDATE SET count = network_usage.count + 1, updated_at = now()
WHERE $limit <= 0 OR network_usage.count < $limit
RETURNING count
```

A filtered conflict returns no row, which the repo maps to `(false, nil)`; the
counter is not incremented. Concurrent workers cannot both pass the last slot.
The RPS/concurrency state remains process-local: a multi-process deployment
still needs a network namespace/firewall forcing workers through one proxy, or
a future distributed limiter.

The business date is computed by the broker in `Asia/Shanghai` via
`internal/timeutil`. `QuotaKey` uses the unit-separator byte so platform IDs
cannot collide.

## Grant lease and revisions

`ExecutionGrant` (owned by this package, aliased in `harness`) now carries:

| field | meaning |
| --- | --- |
| `ExpiresAt` | decision expiry (authoritative upper bound) |
| `LeaseUntil` | short grant lifetime, `now + 60s` capped by `ExpiresAt` |
| `ScopeRevision` | digest of the scope snapshot the decision saw |
| `PolicyRevision` | digest of the automation/constraints snapshot |
| `AuthorizationEpoch` | revocation generation captured at grant build |

`buildGrant` copies both revisions and sets the lease. The harness stamps
`AuthorizationEpoch` from its `capability.RevocationRegistry`. An execution that
outlives the lease must obtain a new decision; the grant is not renewable in
place.

## Validation and revocation

```go
type GrantValidator interface {
    ValidateGrant(ctx context.Context, grant ExecutionGrant) error
}

type ProgramValidator struct {
    Programs    ProgramStore
    Revocations RevocationRegistry
    Now         func() time.Time
}
```

`ProgramValidator` fails closed on, in order:

1. `ExpiresAt` unknown/passed;
2. `LeaseUntil` unknown/passed;
3. Program not `RUNNING`;
4. `Program.StatusFreshAt(now)` false;
5. `Program.Scope.FreshAt(now)` false;
6. `grant.ScopeRevision != Program.Scope.Revision()`;
7. `grant.PolicyRevision != Program.PolicyRevision()`;
8. current revocation epoch != `grant.AuthorizationEpoch`.

`GrantPolicy.Validator` is called after the local expiry/method/target checks
and before `Next`; any error is a Deny. A production proxy must configure
`ProgramValidator`.

The client-side `Egress` enforces the same check: every grant-bound request
calls `GrantValidator.ValidateGrant(ctx, grant)` before acquiring a lease **and
again after the lease is held, immediately before the request is sent**. The
broker wait is bounded by the earlier of `LeaseUntil` and `ExpiresAt`, so a
grant that expires while queued cannot result in a request. A grant-bound
`Egress` without a validator fails closed with `ErrGrantValidatorRequired`;
`AllowUnvalidatedGrantsForTests` is the only escape hatch. This closes the gap
where a long-running tool kept using a grant the proxy would have rejected,
and the authorize-then-queue window.

Grant-bound redirects are denied (`ErrGrantRedirect`): one grant authorizes one
request, and following a redirect would send a second request that was never
authorized, validated or counted.

```go
type RevocationRegistry interface {
    Epoch(ctx context.Context, program domain.ProgramID) (uint64, error)
    Bump(ctx context.Context, program domain.ProgramID) (uint64, error)
}
```

`MemoryRevocationRegistry` is process-local. `PostgresRevocationRegistry{Store:
repo}` persists epochs in `revocation_epochs` (migration `0015`); `Bump` uses a
single `INSERT ... ON CONFLICT DO UPDATE ... RETURNING epoch` so concurrent
bumps cannot return the same value. A registry read error fails closed — an
unreadable epoch is never treated as epoch 0. The CLI wires the durable
registry into both the harness (grant stamping) and the `ProgramValidator`.

Bumps are triggered automatically by `Repo.UpsertProgram`, in the same
transaction as the program write. The sequence is `INSERT ... ON CONFLICT DO
NOTHING` → `SELECT ... FOR UPDATE` → authority comparison → epoch bump →
`UPDATE`: the first statement blocks on the unique index when a concurrent
transaction is creating the same program, so a first-time insert race cannot
skip the comparison (a bare `SELECT ... FOR UPDATE` cannot lock a row that does
not exist yet). The epoch is bumped when the platform identity changes, the
program leaves `RUNNING` (including to `UNKNOWN`), the Scope content revision
changes, or the automation/constraints policy revision changes. A pure
observation refresh with identical authority does not bump. For an immediate
operator kill switch, run:

```bash
bountygrid revoke -program <program-id> -reason "why"
```

The command bumps the durable epoch and appends a `grant.revoked` audit event
(`ActorKind=HUMAN`) in **one transaction** (`Repo.RevokeProgramWithAudit`). The
operator reason is sanitized before the event is validated: a secret-like reason
is stored only as `reason_redacted: true`, `reason_hash`, and
`redaction_kinds`, so the emergency kill switch cannot be blocked by the audit
secret scanner. A genuine payload rejection still rolls the bump back: an
effective revocation without its audit trail must not be committed. Outstanding
grants are rejected on their next request by both `GrantPolicy` and `Egress`,
even after a process restart.

## Deliberate limits

- RPS/concurrency are per-process; the daily quota and revocation epochs can be
  durable.
- `MemoryRevocationRegistry` does not survive a restart and is not shared with
  another process; use `PostgresRevocationRegistry` when revocation is an
  operational control. Bumping the epoch is not yet wired to Reader/KillSwitch
  events (the store exists; the trigger does not).
- `Egress`, proxy-forwarded HTTP and proxy CONNECT all charge the `http`
  interface, so they share one account/program budget. `Request.Interface` is
  available if a future surface needs a separate namespace (for example a
  distinct platform API quota), but splitting HTTP traffic would weaken the
  platform's real ceiling.
- The broker enforces limits; it does not authorize anything. The Action
  Gateway and Policy remain the only source of an ALLOW.
