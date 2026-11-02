# Benchmark Execution Sandbox — Design

- Date: 2026-11-02
- Status: draft for review
- Scope: a two-phase Docker sandbox for the adapter arms of the agent benchmark,
  host-side hidden expectations, and one end-to-end task (`t01_calc_fix`).
- Non-goal: Go/Node images, the remaining 23 hidden suites, CI wiring, WSL
  isolation, and sandboxing the `external` arm.

## 1. Context

The command boundary added on 2026-11-02 (`argv` + `shell=False`, per-token
write-flag rejection, read-path checks) is a **tool-interface constraint**. It
does not isolate the process: acceptance runs `python -m unittest`/`pytest`,
which imports agent-modified code, so arbitrary code execution inside the
worktree remains possible. `scope_pass` is a post-hoc `git status` check, not a
security boundary.

This design adds the OS-level boundary the reviewer asked for, and makes hidden
acceptance possible without shipping expectations into the container.

The host is Windows 11 with Docker Desktop installed (daemon currently stopped)
and WSL2 Ubuntu available. CI is Linux.

## 2. Goals

1. Agent command execution and acceptance run in Linux containers: no network,
   read-only root, only the worktree writable, `.git` read-only.
2. Hidden expectations never enter any container. The agent cannot read the
   values it is being judged against.
3. One task (`t01_calc_fix`) proves the full path: baseline fails, oracle
   passes public + hidden, a shallow public-test-only fix fails hidden.
4. Existing behavior stays available and recorded: `--sandbox none` still runs
   on the host, and every run records which mode it used.
5. Fail closed. A sandbox error never falls back to host execution.

## 3. Non-goals

- Sandboxing the `external` arm (`--agent-cmd`): it is a separate host process
  and does not use the adapter.
- Go/Node images or third-party Python dependencies for the other 23 tasks.
- Hidden acceptance beyond the output-probe form described in section 7.
- GitHub Actions wiring.
- Replacing Docker with WSL2/bubblewrap or Windows-native isolation.

## 4. Threat model and invariants

Trusted: the host runner, the adapter, task JSON, the probe script, the image.
Untrusted: model output and every file the agent creates or modifies.

| ID | Invariant |
|---|---|
| I1 | Neither the agent container nor the acceptance container has network access. |
| I2 | Both containers have a read-only root; only `/work` (and a bounded tmpfs) is writable. |
| I3 | The agent container sees `.git` read-only; it cannot rewrite history or hide changes. |
| I4 | Hidden expectations exist only on the host; the container holds at most a data-free probe. |
| I5 | The agent container is destroyed before the acceptance container starts. |
| I6 | Any sandbox failure marks the run `sandbox_error` and `overall_success=false`; there is no host fallback. |
| I7 | `sandbox.mode` and the image digest are recorded for every run. |
| I8 | The container cannot reach the host adapter, model endpoint, Docker daemon, or any control-plane/Unix socket. `docker.sock`, the host home, and host IPC sockets are never mounted. |
| I9 | Docker's default seccomp profile stays in force; `seccomp=unconfined` is never used. |
| I10 | The agent container is stopped successfully before the acceptance container starts. |

A known gap is recorded in section 12: a source-based hidden test (as opposed
to the probe form) would be readable by agent code during acceptance.

## 5. Architecture

| Path | Responsibility |
|---|---|
| `runner/sandbox/__init__.py` | Exports `DockerSandbox`, `docker_available`, `compare_hidden` |
| `runner/sandbox/docker.py` | `DockerSandbox`: availability check, start/exec/stop, acceptance run, docker arg construction |
| `runner/sandbox/Dockerfile.bench` | `python:3.13-slim` pinned by digest, plus `git`, `coreutils`, `ca-certificates`; copies nothing from the build context; tag `bountygrid-bench:py313` |
| `runner/sandbox/probe.py` | Generic, data-free call probe mounted read-only at `/probe/probe.py`; never baked into the image |
| `runner/agent_openai_tools.py` | `run_command` delegates to an injected `executor`; default remains host subprocess |
| `runner/run_task.py` | `--sandbox {docker,none}` (default `docker`), phase orchestration, hidden comparison, run record |
| `runner/test_sandbox.py` | Arg-builder and probe-comparison unit tests (no Docker) |
| `runner/test_sandbox_integration.py` | `skipUnless(docker_available)` isolation and end-to-end tests |
| `benchmark/tasks/t01_calc_fix.json` | Adds `sandbox_image` and `hidden_cases` |

Flow:

```
host: copy fixture -> init repo
  |
  +--> phase 0: acceptance container
  |      run public acceptance (expect fail)
  |      run probe, compare against hidden_cases (expect mismatch)
  |
  +--> phase 1: agent container (long-lived)
  |      host agent loop -> docker exec -w /work <argv>
  |      .git mounted read-only, no /probe mount
  |      stop and remove in finally
  |
  +--> phase 2: new acceptance container
  |      mount worktree + /probe/probe.py:ro
  |      run public acceptance (expect pass)
  |      run probe, compare against hidden_cases (expect pass)
  |
  +--> host: git status/diff/secrets, write run record
```

## 6. Sandbox API

```python
class ExecResult:
    exit_code: int | None
    stdout: str
    stderr: str
    timed_out: bool

class AcceptanceResult:
    commands: list[ExecResult]   # public acceptance, one per task command
    probe: ExecResult | None     # generic probe run, None when no hidden cases

class DockerSandbox:
    def __init__(self, work: Path, image: str, run_id: str): ...
    def start_agent(self) -> None: ...
    def exec(self, argv: list[str], timeout_s: int) -> ExecResult: ...
    def run_acceptance(self, commands: list[str],
                       probe: Path | None,
                       probe_input: str | None) -> AcceptanceResult: ...
    def stop(self) -> None: ...

def docker_available(timeout_s: int = 5) -> bool: ...
def compare_hidden(probe: ExecResult | None,
                   cases: list[dict]) -> dict: ...
```

`docker_available` runs `docker version --format '{{.Server.Version}}'` with a
short timeout. `start_agent` starts one detached container named
`bench-agent-<run_id>`; `exec` calls `docker exec -w /work` and prefixes the
inner command with `timeout -s KILL <timeout_s>`, so a host-side timeout does
not leave the process running. `stop` runs `docker rm -f` in `finally`.

`stop` is a phase barrier: if it fails, the run is `sandbox_error` and phase 2
does not start. A `docker exec` failure whose stderr carries Docker's own
prefix (`Error response from daemon`, `OCI runtime exec failed`, container not
running) is `sandbox_error`, not a command failure; an ordinary nonzero exit
from the wrapped command stays a command result. There is no
`dangerouslyDisableSandbox` equivalent: a denied or failed command is returned
to the agent, which must adapt inside the workspace.

`compare_hidden` is host-only: it parses the probe's marked result line and
returns `{"declared": bool, "pass": bool, "matched": int, "total": int,
"detail": str}`. It never runs in a container. `run_acceptance` also wraps each
public acceptance command in `timeout -s KILL <timeout_s>` inside `sh -c`, so
the same no-orphan rule applies to tests.

The adapter takes an optional `executor: Callable[[list[str], int], ExecResult]`.
When absent, `run_command` uses the current host subprocess path. The sandbox
executor does not add a shell: `argv` is passed to `docker exec` unchanged.

## 7. Hidden acceptance protocol

Task JSON carries cases, never a test file with assertions:

```json
"sandbox_image": "bountygrid-bench:py313",
"hidden_cases": [
  {"module": "calc.ops", "func": "add", "args": [-7, -8], "expect": -15},
  {"module": "calc.ops", "func": "add", "args": [1000000, 1], "expect": 1000001},
  {"module": "calc.ops", "func": "multiply", "args": [-3, 4], "expect": -12},
  {"module": "calc.ops", "func": "multiply", "args": [0, 9], "expect": 0},
  {"module": "calc.ops", "func": "multiply", "args": [1000000, 1000000], "expect": 1000000000000}
]
```

The host builds `probe_input` as `[{module, func, args}]` and **strips
`expect` before serializing it**; expectations never cross into the container.
The probe receives that list as JSON on stdin. It must consume and validate
the whole input **before** importing any target module, so agent code cannot
read or alter the case stream on first import. Before importing, it also binds
its own references to `json.dumps` and `sys.stdout`. It then calls
`func(*args)` for each case and prints exactly one marked line:

```text
__PROBE_RESULT__[{"ok": true, "value": -15}, {"ok": false, "error": "..."}]
```

Stray prints from the target module are ignored; the host parses only the
marked line. Values must be JSON-serializable. The host compares `value` to
`expect` with exact JSON equality (integers, strings, booleans, `null`, and
exactly representable floats such as `0.75`).

Baseline semantics, recorded per hidden-checked task:

- `baseline_matched`: the probe matched before the agent ran.
- `baseline_error`: the probe crashed or produced no marked line.
- `control_valid = not baseline_matched and not baseline_error`.

A control-invalid run is not silently accepted: it records
`hidden.pass=false`, `error="broken_hidden_control"`, and
`overall_success=false`. A probe crash after the agent is also
`hidden.pass=false` with `detail="probe_error"`; it is never counted as a
mismatch.

The probe file is mounted only in the acceptance container, read-only, at
`/probe/probe.py`, and contains no expectations. The agent sees the runtime
arguments when its function is called, but not the expected values. This is
what makes the check meaningful for the probe class of tests.

## 8. Container configuration

Both containers:

```text
--network none
--read-only
--tmpfs /tmp:rw,size=256m
--cap-drop=ALL
--security-opt no-new-privileges
--pids-limit 256
--memory 2g
--cpus 2
--init
--ipc=private
-e HOME=/tmp
-e PYTHONIOENCODING=utf-8
-e GIT_OPTIONAL_LOCKS=0
--label bench.runner=bountygrid
--label bench.run_id=<run_id>
```

Agent container additionally:

```text
--rm -d
-v <work>:/work:rw
-v <work>/.git:/work/.git:ro
-w /work
--entrypoint sleep <image> infinity
```

Acceptance container additionally:

```text
--rm -i
-v <work>:/work:rw
-v <runner>/sandbox/probe.py:/probe/probe.py:ro
-w /work
-e PYTHONPATH=/work
```

`<runner>` is `docs/benchmarks/agent/runner`. Acceptance commands are trusted
task-spec strings and run through `sh -c` (no login shell); they must not
depend on `.git`, which is not mounted in the acceptance container. The agent
container's `exec` calls remain argv-only. `-i` is required only when hidden
cases are sent to the probe on stdin; without hidden cases it is harmless.

Docker's default seccomp profile stays in force: `--security-opt seccomp` is
never set and `seccomp=unconfined` is forbidden. This is the baseline the three
reference harnesses harden further (Codex/dsh also block `io_uring` and
socket-family escape routes); a custom profile is future work, not a reason to
disable the default. Only the volumes listed above are mounted: never
`/var/run/docker.sock`, the host home, or a host Unix socket.

POSIX hosts pass `--user <host-uid>:<host-gid>` so files created in the bind
mount remain removable by the host. Windows hosts omit `--user` (there is no
host UID); the container runs as root inside the container, and the isolation
still comes from the read-only root and the mount set. The run record notes
`user=root-in-container`.

Git on the host always runs with `-c core.autocrlf=false` and
`-c safe.directory=*`. The agent container is created only after `init_repo`,
so `<work>/.git` exists at mount time. Both containers carry the labels from
the common block; the agent container is removed in `finally`, and a startup
sweep removes any `bench-agent-*` container older than two hours.

The image is built once, before the first docker run:

```text
docker build -f runner/sandbox/Dockerfile.bench -t bountygrid-bench:py313 runner/sandbox
```

## 9. Success metrics and run record

New fields:

```json
"sandbox": {
  "mode": "docker",
  "backend": "docker",
  "enforcement": "full",
  "image": "bountygrid-bench:py313",
  "image_digest": "<docker image id>",
  "agent_container": "bench-agent-<run_id>",
  "user": "1000:1000"
},
"hidden": {
  "declared": true,
  "skipped": false,
  "baseline_matched": false,
  "baseline_error": false,
  "control_valid": true,
  "pass": true,
  "detail": "5/5 cases",
  "error": null
}
```

`hidden.pass` is `null` when the task declares no `hidden_cases`.
`overall_success` requires `hidden.pass is not False` when declared.
`success` stays an alias for `overall_success`. Historical records without
these fields are read as `sandbox.mode=none`, `hidden.declared=false`.

`enforcement` uses the reference harness vocabulary: `full` means the backend
governs every promised effect, `partial` means a known subset is ungoverned.
Docker is `full` for the mounts, rootfs and network it is given; the only
`partial` backends in the reference set are Windows-native runners, which this
phase does not use.

Under `--sandbox none`, hidden checks are **skipped**
(`hidden.skipped=true`, `hidden.pass=null`). The expectations live in the task
JSON inside the repository, and host-side agent code can read it; running the
probe there would be theater. A `none` run therefore never claims a hidden
result, and `overall_success` reflects only the public, scope and completion
criteria.

## 10. Failure semantics

| Condition | Result |
|---|---|
| Docker CLI missing or daemon down | `sandbox_error`, `overall_success=false`, no host fallback |
| Task without `sandbox_image` under `--sandbox docker` | `sandbox_error` |
| `--sandbox docker` with `--arm external` | Refuse to run; require explicit `--sandbox none`, which records `mode=none` |
| Container create/start/exec failure | `sandbox_error` |
| `docker exec` returns Docker's own error text | `sandbox_error`, not a command failure |
| Agent container `stop` fails | `sandbox_error`; phase 2 never starts |
| Command timeout | `exit_code=null`, `timed_out=true`; functional false |
| Probe crash | `hidden.pass=false`, `detail=probe_error` |
| Baseline probe matches or errors | `hidden.pass=false`, `hidden.error=broken_hidden_control`, `overall_success=false` |
| Missing `.git` at start | `sandbox_error` |
| `--sandbox none` | Host execution; `mode=none` recorded; hidden checks skipped; not a security claim |

Migration: `--sandbox docker` is the default, so a run without `--sandbox`
now requires Docker and a task-level `sandbox_image`. The 23 tasks that do not
yet declare one fail closed; `--sandbox none` restores the old host behavior
and records `mode=none`. This is intentional: an unsandboxed run is opt-in,
and it never claims a hidden result.

## 11. Testing

Unit tests (no Docker):

1. Arg builder contains `--network none`, `--read-only`, the tmpfs size, cap
   drops, pids/memory/cpu limits, `--ipc=private`, `/work`, and `.git:ro`.
2. The agent container args contain no probe mount; acceptance args contain
   `/probe/probe.py:ro`.
3. No `seccomp=unconfined`, no `docker.sock`, and no host home mount appear in
   either arg list.
4. Hidden comparison: match, mismatch, probe error, and baseline-match rules.
5. A task without `sandbox_image` is rejected in docker mode.
6. A Docker-level exec error is classified `sandbox_error`, and a failed
   `stop` prevents phase 2.
7. `--sandbox none` records `hidden.skipped=true` and `hidden.pass=null`.

Integration tests (`skipUnless(docker_available)`):

1. `t01` oracle under `--sandbox docker`: baseline public fail + probe
   mismatch, final public + hidden pass, `overall_success=true`.
2. A shallow implementation that special-cases only the four public `add`
   inputs is written into the worktree before phase 2: `hidden.pass=false`.
3. Isolation probes inside the agent container: writing `/etc/x` fails,
   opening a network socket fails, `/probe` does not exist, `/work` is
   writable, `/var/run/docker.sock` does not exist, `$HOME` is `/tmp`, and the
   repository's `benchmark/tasks` path is not present.
4. `--sandbox none` still runs `t01`, records `mode=none`, and skips hidden.
5. An injected `DockerSandbox` whose docker runner fails yields
   `sandbox_error` and does not touch the host worktree.

## 12. Known limitations

1. **The probe is not a cryptographic boundary.** Expected values stay on the
   host, but the target module runs in the probe process and can, in
   principle, monkeypatch the probe's serializer or introspect its frames. The
   probe captures its JSON encoder before importing the target and emits a
   marked line; that raises the bar, it does not make the check unforgeable.
   Unforgeable checking needs an out-of-process verifier with a narrow RPC.
2. **Source-based hidden tests are not hidden at acceptance time.** The probe
   form avoids this; a hidden test file with assertions would still be
   readable by agent code that runs during acceptance. That needs
   deferred-import + Landlock (or an out-of-process verifier) in a later phase.
3. Only the Python image and only `t01` are in scope for this phase.
4. On Windows the container runs as root-in-container (there is no host UID).
   The isolation test asserts `/work` is writable and `/etc` is not; the
   read-only root is what makes root-in-container acceptable.
5. The agent container receives `.git` read-only. With
   `GIT_OPTIONAL_LOCKS=0`, `git diff/log/show` work; an index-refreshing
   command such as `git status` may fail and is returned to the agent as a
   normal command error. The host computes the authoritative diff after the
   agent container is gone.
6. Per-command `docker exec` adds latency (tens to low hundreds of ms). Paired
   comparisons remain valid, but absolute wall-clock values are not comparable
   with pre-sandbox runs.
7. The external arm is unsandboxed by design in this phase.
8. The image pins its base digest, but the `apt` packages installed in the
   Dockerfile (`git`, `coreutils`, `ca-certificates`) float with the Debian
   snapshot. The built image digest is recorded per run, so a result can
   always be traced to the exact image that produced it.
9. Docker isolation shares the host kernel on Linux CI; it is namespace-level
   isolation, not a VM. If the threat model expands beyond a model trying to
   pass tests to kernel-level escapes, switch the runtime to gVisor or Kata.
10. Docker's default seccomp profile is the baseline, not the ceiling: syscalls
    such as `io_uring_setup` may remain allowed. Codex and dsh add explicit
    filters for `io_uring` and socket-family escape routes; a custom profile is
    future work.
11. The control-plane lesson from dsh's CVE-2026-82533 is applied by
    construction: the container has no network, no `docker.sock`, and no host
    IPC socket, so it cannot reach the adapter, the model endpoint, or any
    harness control API to widen its own policy.

## 13. Acceptance criteria

1. `python runner/run_task.py --tasks t01_calc_fix --arm oracle --sandbox docker`
   records baseline public fail, `hidden.baseline_matched=false`,
   `hidden.control_valid=true`, final public and hidden pass, and
   `overall_success=true`.
2. The shallow-fix integration case records `hidden.pass=false` and
   `overall_success=false`.
3. The isolation probes in section 11 pass, including the control-plane
   probes: no `docker.sock`, no repository path, and `$HOME=/tmp`.
4. `--sandbox none` runs the same task, records `mode=none`, and records
   `hidden.skipped=true` with `hidden.pass=null`.
5. A stopped Docker daemon yields `sandbox_error`, no host fallback.
6. Existing runner tests (`test_command_policy.py`, `test_listing.py`,
   `test_run_task.py`) and the Go gate (`go test ./...`, `go vet ./...`,
   `CGO_ENABLED=0 go build ./...`) stay green.

## 14. Decisions

| Decision | Rationale |
|---|---|
| Docker Linux containers | Real filesystem/network isolation, reproducible image, reusable in Linux CI |
| Two containers per task | Hidden mount exists only after the agent container is gone |
| Long-lived agent container + `docker exec` | Avoids 0.5–2 s cold start per tool call, which would distort wall-clock metrics |
| Agent loop stays on host | Keeps the model on loopback and the adapter trusted; only untrusted command execution is containerized |
| Host-side hidden expectations + data-free probe | Prevents the agent from reading the values it is judged against |
| Per-task `sandbox_image`, fail closed | Avoids silently pretending a task is sandboxed when its toolchain is missing |
| Default `--sandbox docker` | Safety is the default; `none` is explicit and recorded |
| No control-plane mounts (I8) | The dsh CVE-2026-82533 escaped through the harness's own API; the container must not be able to reach it |
| Skip hidden checks under `none` | The task JSON is readable on the host, so a `none` run cannot make a hidden claim |
| Sandbox error never becomes a test failure | A broken runner must not be scored as a wrong model answer |

## 15. References

- Codex: [`codex-rs/linux-sandbox/README.md`](https://github.com/openai/codex/blob/main/codex-rs/linux-sandbox/README.md),
  [`codex-rs/mxc-sandbox/README.md`](https://github.com/openai/codex/blob/main/codex-rs/mxc-sandbox/README.md),
  and the sandbox mode prompts under `codex-rs/prompts/templates/permissions/sandbox_mode/`.
- Claude Code: [sandboxing docs](https://code.claude.com/docs/en/sandboxing) and the
  [sandbox-runtime README](https://github.com/anthropic-experimental/sandbox-runtime).
- DeepSeek Harness: [`docs/subsystems/sandbox.md`](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/subsystems/sandbox.md),
  [`packages/sandbox/sandbox-local/README.md`](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/sandbox/sandbox-local/README.md),
  and [`packages/shell/bash-sandbox/README.md`](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/shell/bash-sandbox/README.md).
- dsh control-plane escape: [CVE-2026-82533 summary](https://github.com/lost-rob0t/agentProlog/issues/16).
