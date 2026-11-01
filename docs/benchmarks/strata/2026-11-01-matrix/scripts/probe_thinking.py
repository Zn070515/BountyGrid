"""Probe whether Strata honours the Qwen thinking-off knobs.

Variants:
  baseline                        no extra parameter
  chat_template.enable_thinking   {"chat_template_kwargs":{"enable_thinking":false}}
  reasoning_effort_none           {"reasoning_effort":"none"}
  enable_thinking_top             {"enable_thinking":false}

Reports reasoning/content characters and the server's decode timing for each.
"""

import json
import time
import urllib.request
import uuid

BASE = "http://127.0.0.1:8080"
MODEL = "qwen3.8-flash-next-iq3_s"
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

PROMPT = "Reply with exactly: OK. Run nonce: {}."

VARIANTS = [
    ("baseline", {}),
    ("chat_template.enable_thinking", {"chat_template_kwargs": {"enable_thinking": False}}),
    ("reasoning_effort_none", {"reasoning_effort": "none"}),
    ("reasoning_effort_low", {"reasoning_effort": "low"}),
    ("enable_thinking_top", {"enable_thinking": False}),
]


def run(name, extra):
    body = {
        "model": MODEL,
        "messages": [{"role": "user", "content": PROMPT.format(uuid.uuid4().hex[:8])}],
        "max_tokens": 96,
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    body.update(extra)
    req = urllib.request.Request(
        BASE + "/v1/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json", "Authorization": "Bearer strata-local"},
    )
    t0 = time.perf_counter()
    content, reasoning = [], []
    usage, timings, finish, error = {}, {}, None, None
    try:
        with opener.open(req, timeout=300) as response:
            for line in response:
                text = line.decode("utf-8", "replace").strip()
                if not text.startswith("data: "):
                    continue
                payload = text[6:]
                if payload == "[DONE]":
                    break
                obj = json.loads(payload)
                if obj.get("usage"):
                    usage = obj["usage"]
                if obj.get("timings"):
                    timings = obj["timings"]
                for choice in obj.get("choices") or []:
                    delta = choice.get("delta") or {}
                    if delta.get("content"):
                        content.append(delta["content"])
                    if delta.get("reasoning_content"):
                        reasoning.append(delta["reasoning_content"])
                    if choice.get("finish_reason"):
                        finish = choice["finish_reason"]
    except Exception as exc:
        error = f"{type(exc).__name__}: {exc}"
    wall = time.perf_counter() - t0
    answer = "".join(content)
    return {
        "variant": name,
        "error": error,
        "reasoning_chars": sum(len(x) for x in reasoning),
        "content_chars": len(answer),
        "content": answer[:120],
        "finish_reason": finish,
        "completion_tokens": usage.get("completion_tokens"),
        "wall_s": round(wall, 3),
        "predicted_per_second": timings.get("predicted_per_second"),
        "draft_accept_pct": (
            round(100 * timings["draft_n_accepted"] / timings["draft_n"], 1)
            if timings.get("draft_n")
            else None
        ),
    }


for name, extra in VARIANTS:
    print(json.dumps(run(name, extra), ensure_ascii=False))
