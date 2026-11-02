# -*- coding: utf-8 -*-
"""Docker-backed sandbox for the agent benchmark."""
import dataclasses
import datetime
import os
import shlex
import subprocess
import time
from pathlib import Path

PROBE_CONTAINER_PATH = "/probe/probe.py"
DEFAULT_IMAGE = "bountygrid-bench:py313"
DOCKER_ERROR_PREFIXES = (
    "Error response from daemon",
    "OCI runtime exec failed",
    "No such container",
    "Cannot connect to the Docker daemon",
    "error during connect",
)


@dataclasses.dataclass
class ExecResult:
    exit_code: int | None
    stdout: str = ""
    stderr: str = ""
    timed_out: bool = False


@dataclasses.dataclass
class AcceptanceResult:
    commands: list[ExecResult]
    probe: ExecResult | None


def _host_uid():
    if hasattr(os, "getuid"):
        return f"{os.getuid()}:{os.getgid()}"
    return None


def is_docker_error(result):
    text = (getattr(result, "stderr", "") or "").strip()
    return any(text.startswith(prefix) for prefix in DOCKER_ERROR_PREFIXES)


def docker_available(timeout_s=5):
    try:
        p = subprocess.run(
            ["docker", "version", "--format", "{{.Server.Version}}"],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
            timeout=timeout_s,
        )
    except (OSError, subprocess.TimeoutExpired):
        return False
    return p.returncode == 0


def _run(argv, timeout_s, input_text=None):
    try:
        p = subprocess.run(argv, capture_output=True, text=True, encoding="utf-8",
                           errors="replace", timeout=timeout_s, input=input_text)
        return ExecResult(p.returncode, p.stdout, p.stderr, False)
    except subprocess.TimeoutExpired as exc:
        stdout = exc.stdout or ""
        stderr = exc.stderr or ""
        if isinstance(stdout, bytes):
            stdout = stdout.decode("utf-8", "replace")
        if isinstance(stderr, bytes):
            stderr = stderr.decode("utf-8", "replace")
        return ExecResult(None, stdout, stderr, True)
    except OSError as exc:
        return ExecResult(None, "", str(exc), False)


class DockerSandbox:
    def __init__(self, work, image=DEFAULT_IMAGE, run_id="run",
                 docker_bin="docker", host_uid=None):
        self.work = Path(work).resolve()
        self.image = image
        self.run_id = run_id
        self.docker_bin = docker_bin
        self.name = f"bench-agent-{run_id}"
        self.user = host_uid if host_uid is not None else _host_uid()

    def _user_args(self):
        return ["--user", self.user] if self.user else []

    def common_args(self):
        return [
            "--network", "none",
            "--read-only",
            "--tmpfs", "/tmp:rw,size=256m",
            "--cap-drop=ALL",
            "--security-opt", "no-new-privileges",
            "--pids-limit", "256",
            "--memory", "2g",
            "--cpus", "2",
            "--init",
            "--ipc=private",
            "--label", "bench.runner=bountygrid",
            "--label", f"bench.run_id={self.run_id}",
        ] + self._user_args()

    def agent_args(self):
        return [
            self.docker_bin, "run", "-d", "--rm", "--name", self.name,
            *self.common_args(),
            "-v", f"{self.work}:/work:rw",
            "-v", f"{self.work / '.git'}:/work/.git:ro",
            "-w", "/work",
            "-e", "HOME=/tmp",
            "-e", "PYTHONIOENCODING=utf-8",
            "-e", "GIT_OPTIONAL_LOCKS=0",
            "--entrypoint", "sleep", self.image, "infinity",
        ]

    def exec_args(self, argv, timeout_s):
        return [
            self.docker_bin, "exec", "-w", "/work", self.name,
            "timeout", "-s", "KILL", str(int(timeout_s)), *argv,
        ]

    def acceptance_args(self, command, timeout_s, probe=None, probe_input=None):
        args = [self.docker_bin, "run", "--rm"]
        if probe is not None:
            args.append("-i")
        args += [*self.common_args(),
                 "-v", f"{self.work}:/work:rw"]
        if probe is not None:
            args += ["-v", f"{probe}:/probe/probe.py:ro"]
        args += [
            "-w", "/work",
            "-e", "HOME=/tmp",
            "-e", "PYTHONIOENCODING=utf-8",
            "-e", "GIT_OPTIONAL_LOCKS=0",
            "-e", "PYTHONPATH=/work",
            self.image,
            "sh", "-c",
            f"timeout -s KILL {int(timeout_s)} sh -c {shlex.quote(command)}",
        ]
        return args

    def probe_args(self, probe):
        return [
            self.docker_bin, "run", "--rm", "-i",
            *self.common_args(),
            "-v", f"{self.work}:/work:rw",
            "-v", f"{probe}:/probe/probe.py:ro",
            "-w", "/work",
            "-e", "HOME=/tmp",
            "-e", "PYTHONIOENCODING=utf-8",
            "-e", "PYTHONPATH=/work",
            self.image, "python", PROBE_CONTAINER_PATH,
        ]

    def start_agent(self):
        result = _run(self.agent_args(), 120)
        if result.exit_code != 0:
            _run([self.docker_bin, "rm", "-f", self.name], 30)
            raise RuntimeError(result.stderr.strip() or "docker run failed")

    def exec(self, argv, timeout_s):
        return _run(self.exec_args(argv, timeout_s), int(timeout_s) + 30)

    def run_acceptance(self, commands, probe, probe_input, timeout_s=120):
        results = [
            _run(self.acceptance_args(command, timeout_s, probe=None), timeout_s + 30)
            for command in commands
        ]
        probe_result = None
        if probe is not None:
            probe_result = _run(self.probe_args(probe), timeout_s + 30,
                                input_text=probe_input or "[]")
        return AcceptanceResult(results, probe_result)

    def stop(self):
        return _run([self.docker_bin, "rm", "-f", self.name], 60)

    def image_digest(self):
        result = _run(
            [self.docker_bin, "image", "inspect", "--format", "{{.Id}}", self.image], 30)
        return result.stdout.strip() if result.exit_code == 0 else None


def docker_exec(container, argv, timeout_s, docker_bin="docker"):
    return _run(
        [docker_bin, "exec", "-w", "/work", container,
         "timeout", "-s", "KILL", str(int(timeout_s)), *argv],
        int(timeout_s) + 30,
    )


def sweep_stale(max_age_s=7200, docker_bin="docker"):
    """Remove labelled agent containers older than ``max_age_s``."""
    listing = _run([docker_bin, "ps", "--filter", "label=bench.runner=bountygrid",
                    "--format", "{{.Names}}"], 30)
    if listing.exit_code != 0:
        return 0
    now = time.time()
    removed = 0
    for name in listing.stdout.split():
        info = _run([docker_bin, "inspect", "--format", "{{.State.StartedAt}}", name], 15)
        if info.exit_code != 0:
            continue
        try:
            started = datetime.datetime.fromisoformat(
                info.stdout.strip().replace("Z", "+00:00")).timestamp()
        except ValueError:
            continue
        if now - started > max_age_s:
            _run([docker_bin, "rm", "-f", name], 30)
            removed += 1
    return removed
