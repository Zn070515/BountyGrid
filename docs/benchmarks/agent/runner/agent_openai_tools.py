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

SYSTEM = """You are a coding agent working inside a single repository.
Use the provided tools to inspect and edit files. Do not modify test files unless asked.
When the task is done, reply with a short summary and no tool calls.
All paths are relative to the repository root."""

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

DENY = re.compile(r"(rm\s+-rf\s+/|format\s+[a-z]:|shutdown|reboot|curl\s|wget\s|Invoke-WebRequest|git\s+push|git\s+reset\s+--hard)", re.I)


def safe_path(workdir, rel):
    p = (workdir / (rel or ".")).resolve()
    root = workdir.resolve()
    if root != p and root not in p.parents:
        raise ValueError(f"path escapes workdir: {rel}")
    return p


def normalized(rel):
    return str(Path(rel).as_posix())


def tool_call(workdir, name, args, allowed):
    if name == "list_files":
        p = safe_path(workdir, args.get("path", "."))
        out = []
        for x in sorted(p.rglob("*")):
            if any(part in ("__pycache__", ".git", ".pytest_cache") for part in x.parts):
                continue
            out.append(str(x.relative_to(workdir)).replace("\\", "/"))
            if len(out) >= 300:
                break
        return "\n".join(out) or "(empty)"
    if name == "read_file":
        p = safe_path(workdir, args["path"])
        start = max(1, int(args.get("start", 1)))
        limit = min(400, int(args.get("limit", 200)))
        lines = p.read_text(encoding="utf-8", errors="replace").splitlines()
        chunk = lines[start - 1:start - 1 + limit]
        return f"{p.relative_to(workdir)} lines {start}-{start+len(chunk)-1} of {len(lines)}\n" + "\n".join(chunk)
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
        cmd = args["command"]
        if DENY.search(cmd):
            return "ERROR: command denied by safety policy"
        env = os.environ.copy()
        env["PYTHONIOENCODING"] = "utf-8"
        env["PYTHONUTF8"] = "1"
        try:
            p = subprocess.run(cmd, cwd=str(workdir), shell=True, capture_output=True, text=True,
                               encoding="utf-8", errors="replace",
                               timeout=int(args.get("timeout_s", 60)), env=env)
            return f"exit={p.returncode}\nstdout:\n{p.stdout[-3000:]}\nstderr:\n{p.stderr[-2000:]}"
        except subprocess.TimeoutExpired:
            return "ERROR: timeout"
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
    ap.add_argument("--api-key", default=os.environ.get("OPENAI_API_KEY", "local"))
    args = ap.parse_args()
    prompt = args.prompt or os.environ.get(args.prompt_env, "")
    if not prompt:
        raise SystemExit(f"no prompt: pass --prompt or set {args.prompt_env}")

    workdir = Path(args.workdir).resolve()
    allowed = set(x.strip() for x in args.allowed_files.split(",") if x.strip()) or None
    messages = [{"role": "system", "content": SYSTEM}, {"role": "user", "content": prompt}]
    usage = {"input_tokens": 0, "output_tokens": 0, "model": args.model, "steps": 0,
             "tool_calls": 0, "denied_writes": 0,
             "thinking": "off" if args.no_thinking else "on",
             "allowed_files": sorted(allowed) if allowed else None}
    started = time.time()
    final = ""
    for step in range(args.max_steps):
        try:
            resp = chat(args.base_url, args.model, messages,
                        no_thinking=args.no_thinking, max_tokens=args.max_tokens)
        except Exception as e:
            final = f"API_ERROR: {e}"
            break
        u = resp.get("usage") or {}
        usage["input_tokens"] += int(u.get("prompt_tokens") or 0)
        usage["output_tokens"] += int(u.get("completion_tokens") or 0)
        usage["steps"] = step + 1
        msg = (resp.get("choices") or [{}])[0].get("message") or {}
        calls = msg.get("tool_calls") or []
        usage["tool_calls"] += len(calls)
        messages.append({"role": "assistant", "content": msg.get("content") or "", "tool_calls": calls})
        if not calls:
            final = msg.get("content") or ""
            break
        for call in calls:
            fn = (call.get("function") or {})
            name = fn.get("name")
            try:
                targs = json.loads(fn.get("arguments") or "{}")
            except Exception:
                targs = {}
            try:
                result = tool_call(workdir, name, targs, allowed)
            except Exception as e:
                result = f"ERROR: {e}"
            if isinstance(result, str) and result.startswith("ERROR: path not allowed"):
                usage["denied_writes"] += 1
            messages.append({"role": "tool", "tool_call_id": call.get("id"), "content": str(result)[:8000]})
    usage["wall_clock_s"] = round(time.time() - started, 1)
    (workdir / "agent_usage.json").write_text(json.dumps(usage, ensure_ascii=False, indent=1), encoding="utf-8")
    print(final[-2000:])
    return 0 if not final.startswith("API_ERROR") else 2


if __name__ == "__main__":
    sys.exit(main())
