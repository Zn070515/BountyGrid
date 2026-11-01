"""Second fair pass: thinking off vs on, with adequate output budgets.

The first matrix used max_tokens=128 for retrieval and most "failures" were
empty content because reasoning consumed the budget. This pass measures:

- retrieval with thinking OFF (`chat_template_kwargs.enable_thinking=false`),
  16K/32K contexts at 10/50/90% needle depth, 3 runs, max_tokens 64;
- retrieval with thinking ON and max_tokens 512, 2 runs, same contexts/depths;
- structured-output validity with thinking OFF, 10 runs, max_tokens 256;
- content-type decode with thinking OFF, 3 runs each, max_tokens 512.

Unique nonce per prompt; streaming; server `timings` are authoritative.
"""

import json
import os
import statistics
import subprocess
import threading
import time
import urllib.request
import uuid

BASE = "http://127.0.0.1:8080"
MODEL = "qwen3.8-flash-next-iq3_s"
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.environ.get("STRATA_BENCH_OUT") or os.path.join(HERE, "..", "raw", "thinking.jsonl")
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

FILLER = (
    "This section records routine operational detail for the logging pipeline. "
    "The pipeline batches records, validates schema versions, applies retention rules, "
    "and retries transient failures with exponential backoff. Metrics are emitted once "
    "per batch and checkpoints are written every thirty seconds. No customer identifiers "
    "are stored in this layer."
)
CODE = "ZX-4471"
THINK_OFF = {"chat_template_kwargs": {"enable_thinking": False}}


class Sampler(threading.Thread):
    def __init__(self):
        super().__init__(daemon=True)
        self.stop = threading.Event()
        self.gpu, self.vram, self.power, self.temp = [], [], [], []

    def run(self):
        while not self.stop.is_set():
            try:
                out = subprocess.run(
                    ["nvidia-smi", "--query-gpu=utilization.gpu,memory.used,power.draw,temperature.gpu",
                     "--format=csv,noheader,nounits"],
                    capture_output=True, text=True, timeout=5,
                ).stdout.strip()
                parts = [p.strip() for p in out.split(",")]
                if len(parts) == 4:
                    self.gpu.append(float(parts[0]))
                    self.vram.append(float(parts[1]))
                    self.power.append(float(parts[2]))
                    self.temp.append(float(parts[3]))
            except Exception:
                pass
            self.stop.wait(0.5)

    def summary(self):
        def mx(v):
            return max(v) if v else None

        return {"gpu_util_max": mx(self.gpu), "vram_used_max_mib": mx(self.vram),
                "power_max_w": mx(self.power), "temp_max_c": mx(self.temp)}


def stream_call(config, run, prompt, max_tokens, extra=None, expected_section=None, expect_json=False):
    body = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    if extra:
        body.update(extra)
    req = urllib.request.Request(
        BASE + "/v1/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json", "Authorization": "Bearer strata-local"},
    )
    sampler = Sampler()
    sampler.start()
    t0 = time.perf_counter()
    ttft = None
    content, reasoning = [], []
    usage, timings, finish, error = {}, {}, None, None
    try:
        with opener.open(req, timeout=1800) as response:
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
                    piece, think = delta.get("content"), delta.get("reasoning_content")
                    if piece:
                        content.append(piece)
                    if think:
                        reasoning.append(think)
                    if (piece or think) and ttft is None:
                        ttft = time.perf_counter() - t0
                    if choice.get("finish_reason"):
                        finish = choice["finish_reason"]
    except Exception as exc:
        error = f"{type(exc).__name__}: {exc}"
    wall = time.perf_counter() - t0
    sampler.stop.set()
    sampler.join(timeout=5)
    answer = "".join(content)
    result = {
        "config": config, "run": run, "error": error,
        "thinking": "off" if extra else "on",
        "prompt_tokens": usage.get("prompt_tokens"),
        "completion_tokens": usage.get("completion_tokens"),
        "cached_tokens": (usage.get("prompt_tokens_details") or {}).get("cached_tokens"),
        "finish_reason": finish,
        "ttft_s": round(ttft, 3) if ttft is not None else None,
        "wall_s": round(wall, 3),
        "prompt_per_second": timings.get("prompt_per_second"),
        "predicted_per_second": timings.get("predicted_per_second"),
        "draft_n": timings.get("draft_n"),
        "draft_n_accepted": timings.get("draft_n_accepted"),
        "content_chars": len(answer),
        "reasoning_chars": sum(len(x) for x in reasoning),
        "answer_head": answer[:200],
        **sampler.summary(),
    }
    if expected_section is not None:
        result["expected_section"] = expected_section
        result["answer_ok"] = CODE in answer and f"section {expected_section}" in answer.lower()
    if expect_json:
        try:
            obj = json.loads(answer)
            ok = (isinstance(obj, dict) and isinstance(obj.get("steps"), list)
                  and len(obj["steps"]) == 3
                  and all(isinstance(s, dict) and {"id", "action"} <= set(s) for s in obj["steps"])
                  and obj.get("risk") in {"low", "medium", "high"})
        except (ValueError, TypeError):
            ok = False
        result["json_valid"] = ok
    with open(OUT, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(result, ensure_ascii=False) + "\n")
    return result


def needle_prompt(sections, depth, nonce):
    needle = max(3, int(sections * depth))
    parts = [
        f"Operational handbook. This document has numbered sections. Run nonce: {nonce}. "
        "The answer to the question at the end appears in exactly one section.\n\n"
    ]
    for i in range(1, sections + 1):
        if i == needle:
            parts.append(
                f"Section {i}. DEPLOYMENT CODE: the deployment code for program vulbox/999 "
                f"is {CODE}. This is the only place the code appears.\n\n"
            )
        else:
            parts.append(f"Section {i}. {FILLER}\n\n")
    parts.append("Question: What is the deployment code for program vulbox/999? "
                 "Reply with exactly `<code> @ section <number>` and nothing else.\n")
    return "".join(parts), needle


def json_prompt(nonce):
    return ("Return only a JSON object with keys `steps` (an array of exactly 3 objects, each "
            "with an integer `id` and a string `action`) and `risk` (one of low, medium, high). "
            f"Do not use markdown fences or any other text. Run nonce: {nonce}.")


def structured_prompt(nonce):
    return ("Output the integers 1 through 500 in order, separated by single spaces, and "
            f"nothing else. Run nonce: {nonce}.")


def prose_prompt(nonce):
    return ("Explain the CAP theorem and its practical trade-offs for a distributed database. "
            f"Target about 600 words. Run nonce: {nonce}.")


def code_prompt(nonce):
    return ("Write a Go function `MergeIntervals(intervals [][]int) [][]int` that merges "
            f"overlapping intervals, followed by a short explanation. Run nonce: {nonce}.")


CONFIGS = []
for sections, label in ((254, "16k"), (508, "32k")):
    for depth, dlabel in ((0.1, "10"), (0.5, "50"), (0.9, "90")):
        CONFIGS.append((f"retrieval-off-{label}-{dlabel}", "needle", (sections, depth), 64, THINK_OFF, 3))
        CONFIGS.append((f"retrieval-on-{label}-{dlabel}", "needle", (sections, depth), 512, None, 2))
CONFIGS.append(("json-off", "json", None, 256, THINK_OFF, 10))
CONFIGS.append(("content-off-structured", "text", structured_prompt, 512, THINK_OFF, 3))
CONFIGS.append(("content-off-prose", "text", prose_prompt, 512, THINK_OFF, 3))
CONFIGS.append(("content-off-code", "text", code_prompt, 512, THINK_OFF, 3))


def aggregate(results):
    out = {}
    for cfg in {r["config"] for r in results}:
        rows = [r for r in results if r["config"] == cfg and not r["error"]]

        def stats(key):
            vals = [r[key] for r in rows if isinstance(r.get(key), (int, float))]
            if not vals:
                return None
            return {"median": round(statistics.median(vals), 1), "min": round(min(vals), 1), "max": round(max(vals), 1)}

        entry = {
            "runs": len(rows), "thinking": rows[0]["thinking"] if rows else None,
            "prompt_tokens": rows[0]["prompt_tokens"] if rows else None,
            "completion_tokens": stats("completion_tokens"),
            "ttft_s": stats("ttft_s"), "wall_s": stats("wall_s"),
            "prompt_per_second": stats("prompt_per_second"),
            "predicted_per_second": stats("predicted_per_second"),
            "content_chars": stats("content_chars"),
            "reasoning_chars": stats("reasoning_chars"),
            "temp_max_c": max((r["temp_max_c"] or 0) for r in rows) if rows else None,
            "vram_used_max_mib": max((r["vram_used_max_mib"] or 0) for r in rows) if rows else None,
            "power_max_w": max((r["power_max_w"] or 0) for r in rows) if rows else None,
            "finish": sorted({r["finish_reason"] for r in rows}),
        }
        drafts = [r for r in rows if r.get("draft_n")]
        if drafts:
            accepted = sum(r["draft_n_accepted"] or 0 for r in drafts)
            total = sum(r["draft_n"] or 0 for r in drafts)
            entry["draft_accept_pct"] = round(100 * accepted / total, 1) if total else None
        answered = [r for r in rows if "answer_ok" in r]
        if answered:
            entry["retrieval_accuracy_pct"] = round(100 * sum(1 for r in answered if r["answer_ok"]) / len(answered), 1)
        if any("json_valid" in r for r in rows):
            entry["json_valid_pct"] = round(100 * sum(1 for r in rows if r.get("json_valid")) / len(rows), 1)
        out[cfg] = entry
    return out


def main():
    open(OUT, "w", encoding="utf-8").close()
    results = []
    for config, kind, argument, max_tokens, extra, repeat in CONFIGS:
        for run in range(1, repeat + 1):
            nonce = uuid.uuid4().hex[:8]
            if kind == "needle":
                sections, depth = argument
                prompt, expected = needle_prompt(sections, depth, nonce)
                results.append(stream_call(config, run, prompt, max_tokens, extra, expected_section=expected))
            elif kind == "json":
                results.append(stream_call(config, run, json_prompt(nonce), max_tokens, extra, expect_json=True))
            else:
                results.append(stream_call(config, run, argument(nonce), max_tokens, extra))
    summary = {"generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
               "aggregate": aggregate(results)}
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    with open(os.path.join(HERE, "..", "raw", "thinking-summary.json"), "w", encoding="utf-8") as fh:
        json.dump({"aggregate": summary["aggregate"], "runs": results}, fh, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
