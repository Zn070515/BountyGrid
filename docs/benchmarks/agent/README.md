# Local coding-agent benchmark (reproducible)

Self-contained copy of the HistoryAnalysis agent benchmark, extended with nine
Go/Python tasks and a paired thinking/scope study. Everything needed to
reproduce a run lives in this directory; no external checkout is required.

## Layout

```text
benchmark/
  fixtures/           10 self-contained projects (calc, textutil, inventory,
                      gocalc, counter, window, paging, ringbuf, reporting)
  tasks/              t01..t27 task definitions (prompt, acceptance, allowed files)
  solutions/          oracle patch per task, used only to validate the pipeline
runner/
  run_task.py         copy fixture -> agent -> acceptance -> JSONL record
  agent_openai_tools.py  tool loop; --no-thinking and --allowed-files
  run_paired.py       thinking on/off paired study + scope-enforcement A/B
  run_schema.py       strict JSON-schema and tool-call validity probe
  run_longdepth.py    multi-needle / conflict / distractor / cross-doc / 128K
  check_endpoint.py   endpoint readiness
analysis/analyze_runs.py  per-arm success, McNemar, paired bootstrap
runs/                 generated records (gitignored via *.log only; JSONL kept)
```

## Protocol

- Every run copies the fixture to a fresh temp directory, `git init`, commits a
  baseline, runs the acceptance commands before and after the agent, then checks
  changed files, outside-allowed files, secrets and timeouts.
- Success = acceptance passes **and** no file outside `allowed_files` **and** no
  secret hit **and** the agent did not time out.
- `agent_openai_tools.py` runs a 20-step tool loop at temperature 0. The agent
  command is supplied by the caller, so the same runner can compare any CLI.
- `--allowed-files` enforces the write scope **before** the write; refused
  writes are returned to the model and counted in `agent_usage.json`
  (`denied_writes`).
- `--no-thinking` sends `chat_template_kwargs.enable_thinking=false`; Strata
  ignores the top-level `enable_thinking` field.

## Task set

| ID | Category | Fixture | Acceptance | Allowed files |
|---|---|---|---|---|
| t01 | bugfix | calc (Python) | unittest | calc/ops.py |
| t02 | bugfix | textutil (Python) | unittest | textutil/core.py |
| t03 | feature | inventory (Python) | unittest | inventory/store.py |
| t04 | refactor | inventory (Python) | unittest | inventory/store.py, inventory/summary.py |
| t05 | bugfix | gocalc (Go) | `go test ./...` | gocalc/ops.go |
| t06 | bugfix | counter (Go) | `go test -race ./...` | counter/counter.go |
| t07 | bugfix | window (Python) | unittest | window/range.py |
| t08 | feature | paging (Python) | unittest | paging/paging.py |
| t09 | feature | ringbuf (Go) | `go test ./...` | ringbuf/ring.go |
| t10 | refactor | reporting (Python) | unittest + summary.py exists | reporting/format.py, reporting/summary.py |
| t11 | refactor | shapes (Go) | `go test ./...` | shapes/shape.go, circle.go, rect.go, total.go |
| t12 | bugfix | billing (Python) | unittest | billing/cycle.py |
| t13 | feature | tickets (Python) | unittest | tickets/state.py |
| t24 | feature | longlib (Python, 4 modules) | unittest (27 tests) | longlib/*.py |

`t10` has a structural acceptance command so a no-op cannot pass a refactor
task. `t11` changes an interface across four files; `t12` covers month-end and
leap-year clamping.

## Real local-project slices (difficulty gradient)

These are copies of real code from CADiPy, offipy, ChemRealm and ArtFlow with
one controlled mutation per task; the oracle is the original file. Sources,
commits and exact mutations are in [`PROVENANCE.md`](PROVENANCE.md).

| ID | Source | Difficulty | Focus |
|---|---|---|---|
| t14 | CADiPy `domain/units.py` | easy | length conversion factor |
| t17 | ArtFlow `common/business_rules.py` | easy | inverted registration guard |
| t18 | ArtFlow `core/policies.py` | easy | archived phase must be read-only |
| t15 | offipy `aesthetic.py` | medium | color alpha default |
| t16 | ChemRealm `deterministic-math.ts` | medium | 1-ulp numeric policy |
| t19 | ArtFlow `files/policies.py` | medium | unknown purpose must fail |
| t20 | ArtFlow `common/authority.py` | medium | nested thread-local scope restore |
| t21 | ArtFlow `common/delivery.py` | medium | media-root prefix check |
| t23 | ArtFlow `common/private_test_questionnaire.py` | medium | advancement rounds and file-policy integration |
| t22 | ArtFlow `scripts/verify_private_test_package.py` | hard | PII guard in vote CSV |

t17–t21 use ArtFlow's Django virtualenv for acceptance; see PROVENANCE.

## Long-horizon experiments

`t24_longlib_full` implements four modules (text, stats, store, CLI, 27 tests) in
one prompt with `max_steps=40`. Three execution modes are available:

| Mode | Runner | State carried between stages |
|---|---|---|
| Single prompt | `run_paired.py` with `BENCH_TASKS=t24_longlib_full` | one conversation |
| Staged, fresh process | `run_staged.py` | repository + deterministic `HANDOFF.md` |
| Multi-turn, same session | `run_multiturn.py` | one conversation with four user turns |

The staged runner writes `HANDOFF.md` (completed stages, changed files, last
full-suite status, next instruction) before each stage, so resumption does not
depend on conversation memory.

Results and the three-way comparison are in
[`long-tasks-2026-11-02.md`](long-tasks-2026-11-02.md). All six runs passed;
same-session multi-turn was 2–3× cheaper than fresh-process handoff for this
four-module task.

## Weakness suite (2026-11-02)

Three tasks target the failure modes visible in the v2 rerun:

| Task | Target | Execution |
|---|---|---|
| `t25_ledger_termination` | locate a one-line fix in a longer package; completion vs step cap | host |
| `t26_money_rounding` | float/decimal rounding; public tests plus a 4-case hidden probe | Docker (`sandbox_image`) |
| `t27_config_concurrency` | constraint retention and per-instance state under threads | host |

Run the suite with:

```powershell
$env:BENCH_TASKS='t25_ledger_termination,t26_money_rounding,t27_config_concurrency'
$env:BENCH_REPS='2'
$env:BENCH_OUT='runs/paired-weakness-v2.jsonl'
$env:BENCH_SUMMARY='runs/paired-weakness-summary-v2.json'
python -I -X utf8 runner/run_paired.py
```

`t26` is the second task with `sandbox_image` and `hidden_cases`; run it with
`--sandbox docker` to exercise the hidden probe. The paired runner still uses
the host path for all tasks. The first run scored 12/12 in host mode; the
Docker hidden run scored `on` 4/4 hidden but hit the 20-step cap, `off` 4/4
hidden in 6 steps. See
[`summary-2026-11-02-v2.md`](summary-2026-11-02-v2.md) section 8 for the
full table.

## Harness fixes (2026-11-02)

- **Split success metrics.** `run_task.py` now records `functional_pass`,
  `scope_pass`, `agent_completed`, `step_cap_hit` and `overall_success`;
  `success` means functional + scope + the agent ended normally (not a step
  cap). A test-passing run that exhausted its step budget no longer counts as a
  completed agent.
- **Completion status and baseline gate.** The adapter records
  `completion_status` (`STOPPED_NORMALLY`, `STEP_CAP`, `TOKEN_TRUNCATED`,
  `EMPTY_FINAL`, `TOOL_ERROR`, `TIMEOUT`) from the final `finish_reason` and
  content. `overall_success` requires `STOPPED_NORMALLY`; a run whose baseline
  does not match `expected_baseline` is recorded `baseline_valid=false` and is
  not scored.
- **Command boundary.** `run_command` no longer uses a shell. The command is
  split into an argv list and executed with `shell=False`; only predefined
  (executable, subcommand) pairs are accepted (`python -m
  unittest|pytest|py_compile`, `node --test`, `go test`,
  `git status|diff|log|show`, plus read-only `head`/`tail`/`wc`/`grep`/`rg`/
  `find`/`Select-String`). `dir`/`ls`/`Get-ChildItem` and `type`/`cat` are
  implemented in the adapter, so they work without cmd.exe/PowerShell.
  Shell metacharacters and newlines are rejected. Write-capable flags are
  checked per argv token, including `find -exec/-execdir/-ok/-okdir/-fprint/
  -fprint0/-fprintf/-fls/-delete`, `-o`/`--output`, `--basetemp`,
  coverage/profile flags, `--junitxml` and `--pre`. Read-only executables and
  allowed helpers reject absolute and `..` path arguments, `read_file` refuses
  `.git/**`, and command timeouts are clamped to 1–300 s. This still is not an
  OS sandbox: acceptance imports agent-modified code, so arbitrary code
  execution inside the worktree remains possible. The `external` arm
  (`--agent-cmd`) bypasses the adapter entirely; its scope is checked only
  after the fact. Policy tests: `python runner/test_command_policy.py`.
- **Non-adapter agents.** A cloud CLI without an `agent_usage.json` completion
  flag falls back to its exit status for `agent_completed`; the adapter writes
  `completed`/`step_cap_hit` explicitly. Pass `--agent-max-steps N` so
  `step_cap_hit` is known for a non-adapter command; every record carries
  `agent_max_steps` and `step_cap_known`.
- **Historical data.** Records written before 2026-11-02 use the old `success`
  definition (functional + scope only). New records use `overall_success`,
  which also requires `agent_completed`.
- **Tool-schema metrics.** `run_schema.py` reports undeclared tools, no tool
  call, wrong substitute, correct refusal and truncation separately.
- **Long-context strictness.** `run_longdepth.py` pairs every `@ section N`
  with its nearest code (a swapped section fails), and adds a second injection
  variant (`distractor2`).
- **Bounded listing and reads.** `list_files` walks with `os.scandir` and
  stops at the 300-entry limit instead of sorting a whole `rglob` tree;
  `read_text` streams to the requested window instead of loading the file
  first. On a 10k-file tree the listing dropped from 31 ms to 2.8 ms.
  Regression tests: `python runner/test_listing.py`.
- **Oracle control.** `apply_oracle` touches the copied files and removes
  `__pycache__`/`*.pyc` after `copytree`. Without this, a patch with the same
  size and second-resolution mtime as the baseline file is invisible to git's
  stat cache and Python's pyc header, so the oracle silently re-ran the buggy
  code. Regression tests: `python runner/test_run_task.py`; all 24 oracle runs
  pass.

## Execution sandbox (2026-11-02)

`run_task.py --sandbox docker` (the default) runs every agent command and
acceptance suite in Linux containers:

- agent container: no network, read-only root, only `/work` writable, `.git`
  read-only, no `docker.sock`, no host home;
- a fresh acceptance container after the agent is gone, with the data-free
  probe mounted read-only at `/probe/probe.py`;
- hidden expectations stay in the task JSON on the host and never enter a
  container;
- `--sandbox none` is an explicit, recorded host fallback and skips hidden
  checks because the task JSON is readable on the host.

Build the image once:

```bash
docker build -f runner/sandbox/Dockerfile.bench -t bountygrid-bench:py313 runner/sandbox
```

`t01_calc_fix` and `t26_money_rounding` declare `sandbox_image` in this phase.
The other tasks fail closed under the default; run them with `--sandbox none`
until they get images. `--agent-backend adapter` is required for `agent_trust=TRUSTED_ADAPTER`
and `sandbox_container` matching; the default `external` is recorded
`HOST_UNTRUSTED` and cannot claim container isolation.

Known limits: Docker shares the Linux kernel (use gVisor/Kata for kernel-level
threats); the default seccomp profile is the baseline, not a custom deny list;
a source-based hidden test (as opposed to the probe form) is not hidden from
agent code during acceptance.

Policy and unit tests: `python runner/test_sandbox.py`. Integration and
isolation tests: `python runner/test_sandbox_integration.py` (skipped unless
Docker and the image are available).

Fixed data: `runs/schema-fixed.jsonl`, `runs/schema-fixed-summary.json`,
`runs/longdepth-fixed.jsonl`, `runs/longdepth-summary.json`
(`longdepth-summary-v1.json` is the pre-fix summary).

## Validate the pipeline

```powershell
$env:PYTHONIOENCODING='utf-8'
$env:CGO_ENABLED='1'          # required for go test -race
python -I -X utf8 docs\benchmarks\agent\runner\run_task.py --all --arm oracle --repeat 1 --sandbox none --out $env:TEMP\oracle.jsonl   # 27/27
python -I -X utf8 docs\benchmarks\agent\runner\run_task.py --all --arm mock   --repeat 1 --sandbox none --out $env:TEMP\mock.jsonl     # 0/27
```

## Run the paired study

Start Strata serving `qwen3.8-flash-next-iq3_s` on `127.0.0.1:8080/v1`, then:

```powershell
$env:PYTHONIOENCODING='utf-8'
$env:CGO_ENABLED='1'
$env:NO_PROXY='127.0.0.1,localhost'
python -I -X utf8 docs\benchmarks\agent\runner\run_paired.py
```

It runs the selected tasks × thinking {on,off} × 2 repetitions in
counterbalanced order, plus a scope-enforcement A/B on `t04`/`t10`. The default
output paths overwrite the v1 files, so pass `BENCH_OUT` / `BENCH_SUMMARY` for
a new run. The 2026-11-02 v2 rerun used the four groups t01–t10, t11–t13,
t14–t22 and t24, wrote `runs/*-v2.jsonl` / `runs/*-summary-v2.json`, and is
reported in
[`summary-2026-11-02-v2.md`](summary-2026-11-02-v2.md). Under the
completion-aware metric the real group scores 17/18 (thinking on) and 10/18
(thinking off); see that report for the step-cap breakdown.

The other probes:

```powershell
python -I -X utf8 docs\benchmarks\agent\runner\run_schema.py
python -I -X utf8 docs\benchmarks\agent\runner\run_longdepth.py
```

## Dependencies

- Python 3.12+ (the Strata bundled interpreter works; the task acceptance
  commands use the system `python`).
- Go 1.27+ with `CGO_ENABLED=1` and a C compiler on PATH for `-race`.
- Strata (or any OpenAI-compatible server) with the model named in `run_paired.py`.
- No network access is needed by the fixtures; the only network call is to the
  local model endpoint.

## Provenance

The original runner, adapter, fixtures and tasks came from
`C:\Users\16275\Desktop\HistoryAnalysis\experiments` (2026-11-01). This copy was
made on 2026-11-02 and extended; the original dry-run used `qwen3:8b` and scored
6/8. The benchmark is synthetic and small (13 tasks); it is an initial capability
probe, not a project-level success estimate.
