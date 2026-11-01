"""Fair benchmark matrix for Strata / Qwen3.8-Flash-Next IQ3_S.

Controls:
- streamed calls; the server's `timings` block is authoritative for prefill and
  decode (client wall time includes scheduling overhead);
- one warm-up run discarded;
- unique nonce in every prompt -> `cached_tokens` stays 0;
- fixed `max_tokens` within a comparison;
- 3 runs per point (5 for the structured-output validity test);
- only the variable under test changes (output length, content type, context
  size, needle depth).

Writes raw/matrix.jsonl and raw/matrix-summary.json.
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
OUT = os.environ.get("STRATA_BENCH_OUT") or os.path.join(HERE, "..", "raw", "matrix.jsonl")

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

FILLER = (
    "This section records routine operational detail for the logging pipeline. "
    "The pipeline batches records, validates schema versions, applies retention rules, "
    "and retries transient failures with exponential backoff. Metrics are emitted once "
    "per batch and checkpoints are written every thirty seconds. No customer identifiers "
    "are stored in this layer."
)
CODE = "ZX-4471"


class Sampler(threading.Thread):
    def __init__(self):
        super().__init__(daemon=True)
        self.stop = threading.Event()
        self.gpu, self.vram, self.power, self.temp, self.cpu, self.ram = [], [], [], [], [], []

    def run(self):
        import psutil

        psutil.cpu_percent(None)
        while not self.stop.is_set():
            self.cpu.append(psutil.cpu_percent(None))
            self.ram.append(psutil.virtual_memory().percent)
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

        return {
            "gpu_util_max": mx(self.gpu),
            "vram_used_max_mib": mx(self.vram),
            "power_max_w": mx(self.power),
            "temp_max_c": mx(self.temp),
            "cpu_pct_max": mx(self.cpu),
            "ram_pct_max": mx(self.ram),
        }


def stream_call(config, run, prompt, max_tokens, expected_section=None, expect_json=False):
    body = json.dumps({
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "stream": True,
        "stream_options": {"include_usage": True},
    }).encode("utf-8")
    req = urllib.request.Request(
        BASE + "/v1/chat/completions",
        data=body,
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
                    piece = delta.get("content")
                    think = delta.get("reasoning_content")
                    if piece:
                        content.append(piece)
                    if think:
                        reasoning.append(think)
                    if (piece or think) and ttft is None:
                        ttft = time.perf_counter() - t0
                    if choice.get("finish_reason"):
                        finish = choice["finish_reason"]
    except Exception as exc:  # record a failed point instead of aborting the matrix
        error = f"{type(exc).__name__}: {exc}"
    wall = time.perf_counter() - t0
    sampler.stop.set()
    sampler.join(timeout=5)

    answer = "".join(content)
    result = {
        "config": config,
        "run": run,
        "error": error,
        "prompt_tokens": usage.get("prompt_tokens"),
        "completion_tokens": usage.get("completion_tokens"),
        "cached_tokens": (usage.get("prompt_tokens_details") or {}).get("cached_tokens"),
        "finish_reason": finish,
        "ttft_s": round(ttft, 3) if ttft is not None else None,
        "wall_s": round(wall, 3),
        "prompt_ms": timings.get("prompt_ms"),
        "prompt_per_second": timings.get("prompt_per_second"),
        "predicted_ms": timings.get("predicted_ms"),
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
            ok = (
                isinstance(obj, dict)
                and isinstance(obj.get("steps"), list)
                and len(obj["steps"]) == 3
                and all(isinstance(s, dict) and {"id", "action"} <= set(s) for s in obj["steps"])
                and obj.get("risk") in {"low", "medium", "high"}
            )
        except (ValueError, TypeError):
            ok = False
        result["json_valid"] = ok
    with open(OUT, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(result, ensure_ascii=False) + "\n")
    return result


def decode_prompt(nonce, limit_words=500):
    return (
        f"Output the integers 1 through {limit_words} in order, separated by single spaces, "
        f"and nothing else. Run nonce: {nonce}."
    )


def prose_prompt(nonce):
    return (
        "Explain the CAP theorem and its practical trade-offs for a distributed database. "
        "Target about 600 words. Use concrete systems as examples. "
        f"Run nonce: {nonce}."
    )


def code_prompt(nonce):
    return (
        "Write a Go function `MergeIntervals(intervals [][]int) [][]int` that merges "
        "overlapping intervals, followed by a short explanation of the edge cases. "
        f"Run nonce: {nonce}."
    )


def needle_prompt(sections, nonce, depth=0.9):
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
    parts.append(
        "Question: What is the deployment code for program vulbox/999? "
        "Reply with exactly `<code> @ section <number>` and nothing else.\n"
    )
    return "".join(parts), needle


def json_prompt(nonce):
    return (
        "Return only a JSON object with keys `steps` (an array of exactly 3 objects, each with "
        "an integer `id` and a string `action`) and `risk` (one of low, medium, high). "
        "Do not use markdown fences or any other text. "
        f"Run nonce: {nonce}."
    )


# (config, max_tokens, kind, argument)
CONFIGS = []
for length in (64, 256, 1024, 2048):
    CONFIGS.append((f"decode-{length}", length, "decode", length))
for kind, fn in (("prose", prose_prompt), ("code", code_prompt)):
    CONFIGS.append((f"content-{kind}", 1024, "prompt", fn))
CONFIGS.append(("content-structured", 1024, "decode", 500))
for sections, label in ((16, "1k"), (64, "4k"), (128, "8k"), (254, "16k"), (508, "32k"), (1016, "64k")):
    CONFIGS.append((f"prefill-{label}", 128, "needle", (sections, 0.9)))
for sections, label in ((254, "16k"), (508, "32k")):
    for depth, dlabel in ((0.1, "10"), (0.5, "50"), (0.9, "90")):
        CONFIGS.append((f"depth-{label}-{dlabel}", 128, "needle", (sections, depth)))
CONFIGS.append(("json-validity", 512, "json", None))

RUNS = 3
JSON_RUNS = 5


def build(config, kind, argument):
    nonce = uuid.uuid4().hex[:8]
    expected = None
    if kind == "decode":
        return decode_prompt(nonce, argument), None, False
    if kind == "prompt":
        return argument(nonce), None, False
    if kind == "needle":
        sections, depth = argument
        prompt, needle = needle_prompt(sections, nonce, depth)
        return prompt, needle, False
    if kind == "json":
        return json_prompt(nonce), None, True
    raise ValueError(kind)


def aggregate(results):
    out = {}
    for cfg in {r["config"] for r in results}:
        rows = [r for r in results if r["config"] == cfg and not r["error"]]
        failed = [r for r in results if r["config"] == cfg and r["error"]]

        def stats(key):
            vals = [r[key] for r in rows if isinstance(r.get(key), (int, float))]
            if not vals:
                return None
            return {"median": round(statistics.median(vals), 1), "min": round(min(vals), 1), "max": round(max(vals), 1)}

        entry = {
            "runs": len(rows),
            "failed": len(failed),
            "prompt_tokens": rows[0]["prompt_tokens"] if rows else None,
            "completion_tokens": stats("completion_tokens"),
            "cached_tokens": stats("cached_tokens"),
            "ttft_s": stats("ttft_s"),
            "wall_s": stats("wall_s"),
            "prompt_per_second": stats("prompt_per_second"),
            "predicted_per_second": stats("predicted_per_second"),
            "content_chars": stats("content_chars"),
            "reasoning_chars": stats("reasoning_chars"),
            "gpu_util_max": max((r["gpu_util_max"] or 0) for r in rows) if rows else None,
            "vram_used_max_mib": max((r["vram_used_max_mib"] or 0) for r in rows) if rows else None,
            "power_max_w": max((r["power_max_w"] or 0) for r in rows) if rows else None,
            "temp_max_c": max((r["temp_max_c"] or 0) for r in rows) if rows else None,
            "finish": sorted({r["finish_reason"] for r in rows}),
        }
        drafts = [r for r in rows if r.get("draft_n")]
        if drafts:
            accepted = sum(r["draft_n_accepted"] or 0 for r in drafts)
            total = sum(r["draft_n"] or 0 for r in drafts)
            entry["draft_accept_pct"] = round(100 * accepted / total, 1) if total else None
        if any("answer_ok" in r for r in rows):
            answered = [r for r in rows if "answer_ok" in r]
            entry["retrieval_accuracy_pct"] = round(
                100 * sum(1 for r in answered if r["answer_ok"]) / len(answered), 1
            )
        if any("json_valid" in r for r in rows):
            entry["json_valid_pct"] = round(
                100 * sum(1 for r in rows if r.get("json_valid")) / len(rows), 1
            )
        out[cfg] = entry
    return out


def main():
    open(OUT, "w", encoding="utf-8").close()
    stream_call("warmup", 0, decode_prompt(uuid.uuid4().hex[:8], 50), 32)
    results = []
    for config, max_tokens, kind, argument in CONFIGS:
        runs = JSON_RUNS if kind == "json" else RUNS
        for run in range(1, runs + 1):
            prompt, expected, expect_json = build(config, kind, argument)
            results.append(stream_call(config, run, prompt, max_tokens, expected, expect_json))
    summary = {"generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "aggregate": aggregate(results)}
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    with open(os.path.join(HERE, "..", "raw", "matrix-summary.json"), "w", encoding="utf-8") as fh:
        json.dump({"aggregate": summary["aggregate"], "runs": results}, fh, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
