# -*- coding: utf-8 -*-
"""Strict JSON-schema and tool-call validity probe.

The earlier benchmark only checked a loose JSON shape. This probe validates:

- exact key sets (no unknown fields);
- integer `id` (bool is rejected), action/risk enums;
- every tool call names a declared tool and matches its parameter schema;
- an unknown tool name is a violation (the model must not invent tools).

Thinking on/off are paired; 8 runs each; same max_tokens 256; order alternates.
"""

import json
import os
import statistics
import time
import urllib.request
import uuid

BASE = "http://127.0.0.1:8080/v1"
MODEL = "qwen3.8-flash-next-iq3_s"
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.environ.get("BENCH_SCHEMA_OUT") or os.path.join(HERE, "..", "runs", "schema.jsonl")
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

ACTIONS = {"inspect", "patch", "verify"}
RISKS = {"low", "medium", "high"}

TOOLS = [{
    "type": "function",
    "function": {
        "name": "read_file",
        "description": "Read a text file.",
        "parameters": {
            "type": "object",
            "properties": {"path": {"type": "string"}},
            "required": ["path"],
            "additionalProperties": False,
        },
    },
}]


def call(prompt, tools=None, no_thinking=False, max_tokens=256):
    body = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": 0,
    }
    if tools:
        body["tools"] = tools
        body["tool_choice"] = "auto"
    if no_thinking:
        body["chat_template_kwargs"] = {"enable_thinking": False}
    req = urllib.request.Request(BASE + "/chat/completions", data=json.dumps(body).encode("utf-8"),
                                 headers={"Content-Type": "application/json"})
    t0 = time.perf_counter()
    with opener.open(req, timeout=600) as r:
        data = json.loads(r.read().decode("utf-8"))
    wall = time.perf_counter() - t0
    choice = (data.get("choices") or [{}])[0]
    msg = choice.get("message") or {}
    return {
        "content": msg.get("content") or "",
        "tool_calls": msg.get("tool_calls") or [],
        "usage": data.get("usage") or {},
        "timings": data.get("timings") or {},
        "finish": choice.get("finish_reason"),
        "wall_s": round(wall, 3),
    }


def valid_json_object(text):
    try:
        obj = json.loads(text)
    except (ValueError, TypeError):
        return False
    if not isinstance(obj, dict) or set(obj) != {"steps", "risk"}:
        return False
    steps = obj.get("steps")
    if not isinstance(steps, list) or len(steps) != 3:
        return False
    for step in steps:
        if not isinstance(step, dict) or set(step) != {"id", "action"}:
            return False
        if not isinstance(step["id"], int) or isinstance(step["id"], bool):
            return False
        if not isinstance(step["action"], str) or step["action"] not in ACTIONS:
            return False
    return isinstance(obj.get("risk"), str) and obj["risk"] in RISKS


def valid_tool_call(tool_calls, declared):
    if not tool_calls:
        return False, "no tool call"
    for call_ in tool_calls:
        fn = call_.get("function") or {}
        name = fn.get("name")
        if name not in declared:
            return False, f"unknown tool {name!r}"
        try:
            args = json.loads(fn.get("arguments") or "")
        except (ValueError, TypeError):
            return False, "arguments are not JSON"
        if not isinstance(args, dict) or set(args) != {"path"} or not isinstance(args["path"], str):
            return False, "arguments do not match the schema"
    return True, ""


JSON_PROMPT = (
    "Return only a JSON object with keys steps and risk. steps is an array of exactly "
    "3 objects; each object has exactly the keys id (integer) and action (one of "
    "inspect, patch, verify). risk is one of low, medium, high. No extra keys, no "
    "markdown fences, no other text. Run nonce: {}."
)
TOOL_PROMPT = "Read the file calc/ops.py before editing it. Use the read_file tool. Run nonce: {}."
UNKNOWN_TOOL_PROMPT = "Write the complete file calc/ops.py. Use the write_file tool. Run nonce: {}."


def main():
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    open(OUT, "w", encoding="utf-8").close()
    rows = []
    for run in range(1, 9):
        for no_thinking in (run % 2 == 1, run % 2 == 0):
            mode = "off" if no_thinking else "on"

            r = call(JSON_PROMPT.format(uuid.uuid4().hex[:8]), no_thinking=no_thinking)
            rows.append({"probe": "json", "mode": mode, "run": run,
                         "valid": valid_json_object(r["content"]),
                         "completion_tokens": r["usage"].get("completion_tokens"),
                         "wall_s": r["wall_s"], "finish": r["finish"]})

            r = call(TOOL_PROMPT.format(uuid.uuid4().hex[:8]), tools=TOOLS, no_thinking=no_thinking)
            ok, why = valid_tool_call(r["tool_calls"], {"read_file"})
            rows.append({"probe": "tool-known", "mode": mode, "run": run,
                         "valid": ok, "why": why,
                         "calls": len(r["tool_calls"]),
                         "completion_tokens": r["usage"].get("completion_tokens"),
                         "wall_s": r["wall_s"], "finish": r["finish"]})

            r = call(UNKNOWN_TOOL_PROMPT.format(uuid.uuid4().hex[:8]), tools=TOOLS, no_thinking=no_thinking)
            ok, why = valid_tool_call(r["tool_calls"], {"read_file"})
            # For the unknown-tool probe, a valid outcome is a call to the only
            # declared tool; inventing write_file is a violation.
            invented = any((c.get("function") or {}).get("name") not in {"read_file"} for c in r["tool_calls"])
            rows.append({"probe": "tool-unknown", "mode": mode, "run": run,
                         "valid": not invented, "why": "invented tool" if invented else "",
                         "calls": len(r["tool_calls"]),
                         "completion_tokens": r["usage"].get("completion_tokens"),
                         "wall_s": r["wall_s"], "finish": r["finish"]})

    with open(OUT, "a", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")

    summary = {}
    for probe in ("json", "tool-known", "tool-unknown"):
        for mode in ("on", "off"):
            sel = [r for r in rows if r["probe"] == probe and r["mode"] == mode]
            summary[f"{probe}/{mode}"] = {
                "runs": len(sel),
                "valid": sum(1 for r in sel if r["valid"]),
                "valid_pct": round(100 * sum(1 for r in sel if r["valid"]) / len(sel), 1),
                "median_tokens": statistics.median([r["completion_tokens"] for r in sel if r["completion_tokens"]]),
                "median_wall_s": round(statistics.median([r["wall_s"] for r in sel]), 2),
            }
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
