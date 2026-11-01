"""Smoke-test a local Ollama model on BountyGrid-shaped prompts.

Run from this directory with the model name:
    python -I smoke.py qwen25-0.5b-local
"""

import json
import re
import sys
import time
import urllib.request

# The Windows console defaults to GBK; force UTF-8 so Chinese model output is
# not mangled in captured logs.
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")

MODEL = sys.argv[1] if len(sys.argv) > 1 else "qwen25-0.5b-local"
URL = "http://127.0.0.1:11434/api/chat"

CASES = [
    {
        "name": "chinese-extract",
        "system": "你是漏洞赏金平台的研究助手。只输出一个 JSON 对象，不要解释。",
        "user": (
            "从下面事实中提取陈述与推断，输出 "
            '{"statement":"...","inference":"..."}。\n'
            "事实：项目状态 RUNNING；账号未完成本季度合规考核；因此不能提交任何漏洞。"
        ),
        "expect_json": True,
    },
    {
        "name": "eligibility-decision",
        "system": "你是 BountyGrid 控制层。只输出一个 JSON 对象，不要输出 Markdown。",
        "user": (
            "依据以下事实判断该账号能否对项目 p1 提交漏洞："
            "项目 p1 状态 RUNNING；资产 api.example.com 在 scope 内；"
            "账号已实名；本季度合规考核未完成。"
            '输出 {"eligible":true|false,"reason":"..."}。'
        ),
        "expect_json": True,
    },
    {
        "name": "action-proposal",
        "system": "你是短程 planner 的模型侧。只输出一个 JSON 数组，不要输出 Markdown。",
        "user": (
            "可用动作：VIEW_RULES, READ_SCOPE, ACTIVE_HTTP。"
            "当前只能提出 1 个动作，且 ACTIVE_HTTP 需要 asset。"
            "目标：确认 api.example.com 是否在范围内。"
            '输出 [{"action":"READ_SCOPE","tool":"read_scope","parameters":{},"rationale":"..."}]'
        ),
        "expect_json": True,
    },
]


def extract_json(text):
    text = text.strip()
    text = re.sub(r"^```(?:json)?\s*", "", text)
    text = re.sub(r"\s*```$", "", text)
    for start, end in (("{", "}"), ("[", "]")):
        i = text.find(start)
        j = text.rfind(end)
        if i >= 0 and j > i:
            try:
                return json.loads(text[i : j + 1])
            except json.JSONDecodeError:
                continue
    return None


def run_case(case):
    body = {
        "model": MODEL,
        "stream": False,
        "options": {"temperature": 0, "num_predict": 256, "num_ctx": 4096},
        "messages": [
            {"role": "system", "content": case["system"]},
            {"role": "user", "content": case["user"]},
        ],
    }
    req = urllib.request.Request(
        URL,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
    )
    started = time.time()
    with urllib.request.urlopen(req, timeout=300) as resp:
        data = json.loads(resp.read())
    wall = time.time() - started

    text = data.get("message", {}).get("content", "")
    parsed = extract_json(text) if case["expect_json"] else None
    eval_count = data.get("eval_count", 0)
    eval_ns = data.get("eval_duration", 0) or 1
    return {
        "case": case["name"],
        "wall_s": round(wall, 2),
        "prompt_tokens": data.get("prompt_eval_count", 0),
        "output_tokens": eval_count,
        "tok_per_s": round(eval_count / (eval_ns / 1e9), 1),
        "load_ms": round(data.get("load_duration", 0) / 1e6),
        "json_valid": parsed is not None,
        "parsed": parsed,
        "raw": text,
    }


def main():
    results = []
    for case in CASES:
        try:
            results.append(run_case(case))
        except Exception as exc:  # noqa: BLE001 - smoke script reports the error
            results.append({"case": case["name"], "error": repr(exc)})
    print(json.dumps(results, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
