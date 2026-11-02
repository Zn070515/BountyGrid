# Local coding-agent benchmark (reproducible)

Self-contained copy of the HistoryAnalysis agent benchmark, extended with nine
Go/Python tasks and a paired thinking/scope study. Everything needed to
reproduce a run lives in this directory; no external checkout is required.

## Layout

```text
benchmark/
  fixtures/           10 self-contained projects (calc, textutil, inventory,
                      gocalc, counter, window, paging, ringbuf, reporting)
  tasks/              t01..t10 task definitions (prompt, acceptance, allowed files)
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

## Validate the pipeline

```powershell
$env:PYTHONIOENCODING='utf-8'
$env:CGO_ENABLED='1'          # required for go test -race
python -I -X utf8 docs\benchmarks\agent\runner\run_task.py --all --arm oracle --repeat 1 --out $env:TEMP\oracle.jsonl   # 10/10
python -I -X utf8 docs\benchmarks\agent\runner\run_task.py --all --arm mock   --repeat 1 --out $env:TEMP\mock.jsonl     # 0/10
```

## Run the paired study

Start Strata serving `qwen3.8-flash-next-iq3_s` on `127.0.0.1:8080/v1`, then:

```powershell
$env:PYTHONIOENCODING='utf-8'
$env:CGO_ENABLED='1'
$env:NO_PROXY='127.0.0.1,localhost'
python -I -X utf8 docs\benchmarks\agent\runner\run_paired.py
```

It runs 13 tasks × thinking {on,off} × 2 repetitions in counterbalanced order,
plus a scope-enforcement A/B on `t04`/`t10`, and writes
`runs/paired_2026-11-02.jsonl` and `runs/paired-summary.json`. To run a subset,
set `BENCH_TASKS` to a comma-separated list of task IDs and set `BENCH_OUT` /
`BENCH_SUMMARY` to separate files.

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
