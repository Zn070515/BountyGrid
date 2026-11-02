# -*- coding: utf-8 -*-
"""Minimal tool-calling agent for OpenAI-compatible local servers.

Archived from HistoryAnalysis/experiments/runner and extended for the fairness
study:

- ``--no-thinking`` sends ``chat_template_kwargs.enable_thinking=false`` (Strata
  ignores the top-level ``enable_thinking`` field).
- ``--allowed-files`` enforces the write allowlist *before* a write happens.
  Refused writes are counted in ``agent_usage.json`` (``denied_writes``).
- usage records tool calls, denied writes and the thinking mode.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
# ``python -I`` (used by the other runners) does not put the script directory
# on sys.path, so the sandbox package must be made importable explicitly.
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

SYSTEM = """You are a coding agent working inside a single repository.
Use the provided tools to inspect and edit files. Do not modify test files unless asked.
When the task is done, reply with a short summary and no tool calls.
All paths are relative to the repository root."""

RUN_STATE = {"sandbox_error": None}

TOOLS = [
    {"type": "function", "function": {
        "name": "list_files",
        "description": "List files under a relative directory.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string", "description": "relative directory, default ."}},
            "required": []}}},
    {"type": "function", "function": {
        "name": "read_file",
        "description": "Read a text file. Use start/limit to page large files.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"},
            "start": {"type": "integer", "description": "1-based line number"},
            "limit": {"type": "integer", "description": "max lines"}},
            "required": ["path"]}}},
    {"type": "function", "function": {
        "name": "write_file",
        "description": "Create or overwrite a text file with full content.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}, "content": {"type": "string"}},
            "required": ["path", "content"]}}},
    {"type": "function", "function": {
        "name": "replace_in_file",
        "description": "Replace an exact substring in a file.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}, "old": {"type": "string"}, "new": {"type": "string"}},
            "required": ["path", "old", "new"]}}},
    {"type": "function", "function": {
        "name": "run_command",
        "description": "Run a shell command in the repository root (tests, git status, python).",
        "parameters": {"type": "object", "properties": {
            "command": {"type": "string"}, "timeout_s": {"type": "integer"}},
            "required": ["command"]}}},
]

DENY = re.compile(
    r"(rm\s+-rf\s+/|format\s+[a-z]:|shutdown|reboot|curl\s|wget\s|Invoke-WebRequest|git\s+push|git\s+reset\s+--hard"
    r"|-exec\b|-delete\b|-ok\b|-fprint\b|-fprintf\b|-fls\b|--output\b|(^|\s)-o(\s|$)"
    r"|-coverprofile|--coverprofile|-cpuprofile|-memprofile|-trace\b|--junitxml|--cov-report|--html\b|xargs\b"
    r"|--pre\b)",
    re.I,
)

SHELL_META = re.compile(r"[|<>;&`]|\$\(|[\r\n]")

# Commands run as an argv list with ``shell=False``. There is no shell to parse,
# so a command is described by (executable, subcommand) instead of a regex.
PY_MODULES = {"unittest", "pytest", "py_compile"}
GIT_SUBCOMMANDS = {"status", "diff", "log", "show"}
READ_ONLY_EXES = {"head", "tail", "wc", "grep", "rg", "find",
                  "Select-String"}

# Read-only helpers implemented here so they work without cmd.exe/PowerShell.
INTERNAL_LIST = {"dir", "ls", "Get-ChildItem"}
INTERNAL_READ = {"type", "cat"}

LIST_LIMIT = 300
SKIP_NAMES = {"__pycache__", ".git", ".pytest_cache"}

# Flags that write outside the expected artifacts. Checked per argv token.
FORBIDDEN_FLAGS = {
    # find(1) write primitives
    "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf",
    "-fls", "-delete",
    # output redirection / extra artifacts
    "-o", "--output", "--basetemp", "-coverprofile", "--coverprofile",
    "-cpuprofile", "-memprofile", "-trace", "--trace", "--junitxml",
    "--cov-report", "--html", "--pre", "--pre-glob", "--ext-diff", "--textconv",
}

MIN_COMMAND_TIMEOUT = 1
MAX_COMMAND_TIMEOUT = 300
DEFAULT_COMMAND_TIMEOUT = 60


def clamp_timeout(value):
    try:
        seconds = int(value)
    except (TypeError, ValueError):
        return DEFAULT_COMMAND_TIMEOUT
    return max(MIN_COMMAND_TIMEOUT, min(MAX_COMMAND_TIMEOUT, seconds))


def split_command(cmd):
    """Split a command into argv. Quotes group; backslash is literal."""
    tokens, current, quote = [], [], None
    for ch in cmd:
        if quote:
            if ch == quote:
                quote = None
            else:
                current.append(ch)
        elif ch in ("'", '"'):
            quote = ch
        elif ch.isspace():
            if current:
                tokens.append("".join(current))
                current = []
        else:
            current.append(ch)
    if quote:
        raise ValueError("unbalanced quote")
    if current:
        tokens.append("".join(current))
    return tokens


def forbidden_flag(token):
    return token.split("=", 1)[0].lower() in FORBIDDEN_FLAGS


def path_escape(token):
    """True for absolute or parent-traversing path arguments."""
    if re.match(r"^(?:/|[A-Za-z]:[\\/]|\\\\)", token):
        return True
    return ".." in re.split(r"[\\/]", token)


def check_command(cmd):
    """Return (argv, None) if the command is allowed, else (None, reason)."""
    if not isinstance(cmd, str) or not cmd.strip():
        return None, "empty command"
    if SHELL_META.search(cmd):
        return None, "shell metacharacters are not allowed"
    if DENY.search(cmd):
        return None, "command denied by safety policy"
    try:
        argv = split_command(cmd)
    except ValueError as exc:
        return None, str(exc)
    if not argv:
        return None, "empty command"
    for token in argv[1:]:
        if token.startswith("-") and forbidden_flag(token):
            return None, f"flag not allowed: {token}"
    exe = argv[0].lower()
    if exe in INTERNAL_LIST or exe in INTERNAL_READ:
        flags = [a for a in argv[1:] if a.startswith("-") or a.startswith("/")]
        if flags:
            return None, f"flags are not supported for {argv[0]}: {flags[0]}"
        if len(argv) > 2:
            return None, f"{argv[0]} accepts at most one path"
        if exe in INTERNAL_READ and len(argv) < 2:
            return None, f"{argv[0]} requires a path"
        return argv, None
    if exe in ("python", "python.exe"):
        if len(argv) < 3 or argv[1] != "-m" or argv[2] not in PY_MODULES:
            return None, "only 'python -m unittest|pytest|py_compile' is allowed"
        probe = argv[3:]
    elif exe == "node":
        if len(argv) < 2 or argv[1] != "--test":
            return None, "only 'node --test' is allowed"
        probe = argv[2:]
    elif exe == "go":
        if len(argv) < 2 or argv[1] != "test":
            return None, "only 'go test' is allowed"
        probe = argv[2:]
    elif exe == "git":
        if len(argv) < 2 or argv[1] not in GIT_SUBCOMMANDS:
            return None, "only read-only git subcommands are allowed"
        probe = argv[2:]
    elif exe in READ_ONLY_EXES:
        probe = argv[1:]
    else:
        return None, f"command not allowed: {argv[0]}"
    for token in probe:
        if not token.startswith("-") and path_escape(token):
            return None, f"path escapes workdir: {token}"
    return argv, None


def safe_path(workdir, rel):
    p = (workdir / (rel or ".")).resolve()
    root = workdir.resolve()
    if root != p and root not in p.parents:
        raise ValueError(f"path escapes workdir: {rel}")
    return p


def normalized(rel):
    return str(Path(rel).as_posix())


def read_path(workdir, rel):
    """Resolve a path for reading; refuse repository internals."""
    p = safe_path(workdir, rel)
    parts = p.relative_to(workdir.resolve()).parts
    if ".git" in parts:
        raise ValueError(f"reading repository internals is not allowed: {rel}")
    return p


def scandir_sorted(path):
    """Sorted directory entries; an unreadable directory yields nothing."""
    try:
        with os.scandir(path) as it:
            return iter(sorted(it, key=lambda entry: entry.name))
    except OSError:
        return iter(())


def list_files(workdir, rel=".", limit=LIST_LIMIT):
    """Bounded depth-first listing; stops once ``limit`` entries are found."""
    try:
        p = safe_path(workdir, rel)
    except Exception as exc:
        return f"ERROR: {exc}"
    if not p.exists():
        return f"ERROR: not found: {rel}"
    try:
        limit = max(1, int(limit))
    except (TypeError, ValueError):
        limit = LIST_LIMIT
    root = workdir.resolve()
    out = []
    stack = [scandir_sorted(p)]
    while stack and len(out) < limit:
        try:
            entry = next(stack[-1])
        except StopIteration:
            stack.pop()
            continue
        if entry.name in SKIP_NAMES:
            continue
        out.append(str(Path(entry.path).relative_to(root)).replace("\\", "/"))
        if len(out) >= limit:
            break
        if entry.is_dir(follow_symlinks=False):
            stack.append(scandir_sorted(entry.path))
    return "\n".join(out) or "(empty)"


def read_text(workdir, rel, start=1, limit=200):
    """Read a window of lines without loading the whole file into memory."""
    try:
        p = read_path(workdir, rel)
        start = max(1, int(start))
        limit = min(400, int(limit))
        chunk = []
        total = 0
        with p.open("r", encoding="utf-8", errors="replace") as fh:
            for lineno, line in enumerate(fh, start=1):
                total = lineno
                if start <= lineno < start + limit:
                    chunk.append(line.rstrip("\r\n"))
    except Exception as exc:
        return f"ERROR: {exc}"
    return f"{p.relative_to(workdir)} lines {start}-{start+len(chunk)-1} of {total}\n" + "\n".join(chunk)


def tool_call(workdir, name, args, allowed, executor=None):
    if name == "list_files":
        return list_files(workdir, args.get("path", "."))
    if name == "read_file":
        return read_text(workdir, args["path"], args.get("start", 1), args.get("limit", 200))
    if name in ("write_file", "replace_in_file"):
        rel = normalized(args["path"])
        if allowed is not None and rel not in allowed:
            return f"ERROR: path not allowed by the task scope: {rel}"
        p = safe_path(workdir, rel)
    if name == "write_file":
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(args["content"], encoding="utf-8")
        return f"wrote {p.relative_to(workdir)} ({len(args['content'])} chars)"
    if name == "replace_in_file":
        text = p.read_text(encoding="utf-8")
        old, new = args["old"], args["new"]
        if old not in text:
            return "ERROR: old string not found"
        p.write_text(text.replace(old, new, 1), encoding="utf-8")
        return f"replaced in {p.relative_to(workdir)}"
    if name == "run_command":
        argv, reason = check_command(args.get("command", ""))
        if argv is None:
            return f"ERROR: command denied by safety policy ({reason})"
        exe = argv[0].lower()
        if exe in INTERNAL_LIST:
            return list_files(workdir, argv[1] if len(argv) > 1 else ".")
        if exe in INTERNAL_READ:
            return read_text(workdir, argv[1], limit=400)
        if executor is not None:
            try:
                result = executor(
                    argv,
                    clamp_timeout(args.get("timeout_s", DEFAULT_COMMAND_TIMEOUT)),
                )
            except Exception as exc:
                return f"ERROR: {exc}"
            if getattr(result, "timed_out", False):
                return "ERROR: timeout"
            return (f"exit={result.exit_code}\n"
                    f"stdout:\n{result.stdout[-3000:]}\n"
                    f"stderr:\n{result.stderr[-2000:]}")
        env = os.environ.copy()
        env["PYTHONIOENCODING"] = "utf-8"
        env["PYTHONUTF8"] = "1"
        try:
            p = subprocess.run(argv, cwd=str(workdir), shell=False, capture_output=True, text=True,
                               encoding="utf-8", errors="replace",
                               timeout=clamp_timeout(args.get("timeout_s", DEFAULT_COMMAND_TIMEOUT)),
                               env=env)
            return f"exit={p.returncode}\nstdout:\n{p.stdout[-3000:]}\nstderr:\n{p.stderr[-2000:]}"
        except subprocess.TimeoutExpired:
            return "ERROR: timeout"
        except FileNotFoundError:
            return f"ERROR: executable not found: {argv[0]}"
    return f"ERROR: unknown tool {name}"


def chat(base_url, model, messages, timeout=300, no_thinking=False, max_tokens=0):
    body = {"model": model, "messages": messages, "tools": TOOLS,
            "tool_choice": "auto", "temperature": 0}
    if no_thinking:
        body["chat_template_kwargs"] = {"enable_thinking": False}
    if max_tokens:
        body["max_tokens"] = max_tokens
    req = urllib.request.Request(base_url.rstrip("/") + "/chat/completions",
                                 data=json.dumps(body).encode("utf-8"),
                                 headers={"Content-Type": "application/json"})
    # The endpoint is local; never send it through an environment proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(req, timeout=timeout) as r:
        return json.loads(r.read().decode("utf-8"))


def run_multiturn(args, executor=None):
    """Run consecutive user prompts in one conversation (same session)."""
    prompts = json.loads(Path(args.prompts_file).read_text(encoding="utf-8"))
    if not isinstance(prompts, list) or not prompts or not all(
        isinstance(prompt, str) and prompt.strip() for prompt in prompts
    ):
        raise SystemExit("--prompts-file must contain a non-empty list of non-empty strings")

    workdir = Path(args.workdir).resolve()
    allowed = set(x.strip() for x in args.allowed_files.split(",") if x.strip()) or None
    messages = [{"role": "system", "content": SYSTEM}]
    usage = {"input_tokens": 0, "output_tokens": 0, "model": args.model, "steps": 0,
             "tool_calls": 0, "denied_writes": 0, "step_usage": [], "turns": [],
             "thinking": "off" if args.no_thinking else "on",
             "allowed_files": sorted(allowed) if allowed else None,
             "sandbox_container": args.sandbox_container or None}
    started = time.time()
    final = ""
    steps_used = 0
    aborted = False

    for turn_index, turn_prompt in enumerate(prompts, start=1):
        messages.append({"role": "user", "content": turn_prompt})
        turn_input = turn_output = turn_steps = turn_tools = 0
        turn_done = False
        while steps_used < args.max_steps:
            try:
                resp = chat(args.base_url, args.model, messages,
                            no_thinking=args.no_thinking, max_tokens=args.max_tokens)
            except Exception as e:
                final = f"API_ERROR: {e}"
                aborted = True
                break
            u = resp.get("usage") or {}
            step_input = int(u.get("prompt_tokens") or 0)
            step_output = int(u.get("completion_tokens") or 0)
            usage["input_tokens"] += step_input
            usage["output_tokens"] += step_output
            turn_input += step_input
            turn_output += step_output
            steps_used += 1
            turn_steps += 1
            usage["steps"] = steps_used
            msg = (resp.get("choices") or [{}])[0].get("message") or {}
            calls = msg.get("tool_calls") or []
            usage["tool_calls"] += len(calls)
            turn_tools += len(calls)
            usage["step_usage"].append({
                "step": steps_used, "turn": turn_index,
                "input_tokens": step_input, "output_tokens": step_output,
                "tools": [(call.get("function") or {}).get("name") for call in calls],
            })
            messages.append({"role": "assistant", "content": msg.get("content") or "", "tool_calls": calls})
            if not calls:
                final = msg.get("content") or ""
                turn_done = True
                break
            for call in calls:
                fn = (call.get("function") or {})
                name = fn.get("name")
                try:
                    targs = json.loads(fn.get("arguments") or "{}")
                except Exception:
                    targs = {}
                try:
                    result = tool_call(workdir, name, targs, allowed, executor=executor)
                except Exception as e:
                    result = f"ERROR: {e}"
                if isinstance(result, str) and result.startswith("ERROR: path not allowed"):
                    usage["denied_writes"] += 1
                messages.append({"role": "tool", "tool_call_id": call.get("id"), "content": str(result)[:8000]})
        usage["turns"].append({
            "turn": turn_index, "input_tokens": turn_input, "output_tokens": turn_output,
            "steps": turn_steps, "tool_calls": turn_tools, "completed": turn_done,
        })
        if aborted or not turn_done:
            break

    usage["completed"] = len(usage["turns"]) == len(prompts) and all(
        turn["completed"] for turn in usage["turns"]
    )
    usage["step_cap_hit"] = (not usage["completed"] and not aborted)
    usage["wall_clock_s"] = round(time.time() - started, 1)
    usage["sandbox_error"] = RUN_STATE["sandbox_error"]
    (workdir / "agent_usage.json").write_text(json.dumps(usage, ensure_ascii=False, indent=1), encoding="utf-8")
    print(final[-2000:])
    return 0 if not final.startswith("API_ERROR") else 2


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--workdir", required=True)
    ap.add_argument("--prompt", default=None, help="task prompt; if omitted, read --prompt-env")
    ap.add_argument("--prompt-env", default="BENCH_PROMPT")
    ap.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    ap.add_argument("--model", required=True)
    ap.add_argument("--max-steps", type=int, default=40)
    ap.add_argument("--max-tokens", type=int, default=0)
    ap.add_argument("--no-thinking", action="store_true")
    ap.add_argument("--allowed-files", default="",
                    help="comma-separated write allowlist; empty means no enforcement")
    ap.add_argument("--prompts-file", default="",
                    help="JSON list of consecutive user prompts in the same session")
    ap.add_argument("--sandbox-container",
                    default=os.environ.get("BENCH_SANDBOX_CONTAINER", ""))
    ap.add_argument("--api-key", default=os.environ.get("OPENAI_API_KEY", "local"))
    args = ap.parse_args()
    RUN_STATE["sandbox_error"] = None
    executor = None
    if args.sandbox_container:
        from sandbox.docker import docker_exec, is_docker_error

        def executor(argv, timeout_s):
            result = docker_exec(args.sandbox_container, argv, timeout_s)
            if is_docker_error(result):
                RUN_STATE["sandbox_error"] = result.stderr.strip()
                raise RuntimeError(result.stderr.strip())
            return result

    if args.prompts_file:
        return run_multiturn(args, executor)
    prompt = args.prompt or os.environ.get(args.prompt_env, "")
    if not prompt:
        raise SystemExit(f"no prompt: pass --prompt or set {args.prompt_env}")

    workdir = Path(args.workdir).resolve()
    allowed = set(x.strip() for x in args.allowed_files.split(",") if x.strip()) or None
    messages = [{"role": "system", "content": SYSTEM}, {"role": "user", "content": prompt}]
    usage = {"input_tokens": 0, "output_tokens": 0, "model": args.model, "steps": 0,
             "tool_calls": 0, "denied_writes": 0, "step_usage": [],
             "thinking": "off" if args.no_thinking else "on",
             "allowed_files": sorted(allowed) if allowed else None,
             "sandbox_container": args.sandbox_container or None}
    started = time.time()
    final = ""
    completed = False
    for step in range(args.max_steps):
        try:
            resp = chat(args.base_url, args.model, messages,
                        no_thinking=args.no_thinking, max_tokens=args.max_tokens)
        except Exception as e:
            final = f"API_ERROR: {e}"
            break
        u = resp.get("usage") or {}
        step_input = int(u.get("prompt_tokens") or 0)
        step_output = int(u.get("completion_tokens") or 0)
        usage["input_tokens"] += step_input
        usage["output_tokens"] += step_output
        usage["steps"] = step + 1
        msg = (resp.get("choices") or [{}])[0].get("message") or {}
        calls = msg.get("tool_calls") or []
        usage["tool_calls"] += len(calls)
        usage["step_usage"].append({
            "step": step + 1,
            "input_tokens": step_input,
            "output_tokens": step_output,
            "tools": [(call.get("function") or {}).get("name") for call in calls],
        })
        messages.append({"role": "assistant", "content": msg.get("content") or "", "tool_calls": calls})
        if not calls:
            final = msg.get("content") or ""
            completed = True
            break
        for call in calls:
            fn = (call.get("function") or {})
            name = fn.get("name")
            try:
                targs = json.loads(fn.get("arguments") or "{}")
            except Exception:
                targs = {}
            try:
                result = tool_call(workdir, name, targs, allowed, executor=executor)
            except Exception as e:
                result = f"ERROR: {e}"
            if isinstance(result, str) and result.startswith("ERROR: path not allowed"):
                usage["denied_writes"] += 1
            messages.append({"role": "tool", "tool_call_id": call.get("id"), "content": str(result)[:8000]})
    usage["completed"] = completed
    usage["step_cap_hit"] = (not completed and not final.startswith("API_ERROR"))
    usage["wall_clock_s"] = round(time.time() - started, 1)
    usage["sandbox_error"] = RUN_STATE["sandbox_error"]
    (workdir / "agent_usage.json").write_text(json.dumps(usage, ensure_ascii=False, indent=1), encoding="utf-8")
    print(final[-2000:])
    return 0 if not final.startswith("API_ERROR") else 2


if __name__ == "__main__":
    sys.exit(main())
