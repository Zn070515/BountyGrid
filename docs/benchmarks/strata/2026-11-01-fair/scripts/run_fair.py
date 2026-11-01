"""Fair Strata benchmark: fixed budgets, 3 repetitions, cold prompts.

Every call is streamed so the server's own `timings` block separates prefill
(`prompt_per_second`) from decode (`predicted_per_second`). Each prompt carries
a unique nonce so the prefix cache does not flatter later runs.

Run: python run_fair.py
Writes raw/fair.jsonl and prints a per-config aggregate.
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
OUT = os.environ.get("STRATA_BENCH_OUT") or os.path.join(HERE, "..", "raw", "fair.jsonl")

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


class Sampler(threading.Thread):
    def __init__(self):
        super().__init__(daemon=True)
        self.stop = threading.Event()
        self.gpu_util, self.vram, self.power, self.temp = [], [], [], []
        self.cpu, self.ram_pct = [], []

    def run(self):
        import psutil

        psutil.cpu_percent(None)
        while not self.stop.is_set():
            self.cpu.append(psutil.cpu_percent(None))
            self.ram_pct.append(psutil.virtual_memory().percent)
            try:
                out = subprocess.run(
                    ["nvidia-smi", "--query-gpu=utilization.gpu,memory.used,power.draw,temperature.gpu",
                     "--format=csv,noheader,nounits"],
                    capture_output=True, text=True, timeout=5,
                ).stdout.strip()
                parts = [p.strip() for p in out.split(",")]
                if len(parts) == 4:
                    self.gpu_util.append(float(parts[0]))
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
            "gpu_util_max": mx(self.gpu_util),
            "vram_used_max_mib": mx(self.vram),
            "power_max_w": mx(self.power),
            "temp_max_c": mx(self.temp),
            "cpu_pct_max": mx(self.cpu),
            "ram_pct_max": mx(self.ram_pct),
        }


def stream_call(config, run, prompt, max_tokens):
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
    usage, timings, finish = {}, {}, None
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
    wall = time.perf_counter() - t0
    sampler.stop.set()
    sampler.join(timeout=5)

    result = {
        "config": config,
        "run": run,
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
        "answer_head": "".join(content)[:200],
        "reasoning_chars": sum(len(x) for x in reasoning),
        **sampler.summary(),
    }
    with open(OUT, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(result, ensure_ascii=False) + "\n")
    return result


DECODE_PROMPT = (
    "Output the integers 1 through 500 in order, separated by single spaces, "
    "and nothing else. Run nonce: {nonce}."
)

FILLER = (
    "This section records routine operational detail for the logging pipeline. "
    "The pipeline batches records, validates schema versions, applies retention rules, "
    "and retries transient failures with exponential backoff. Metrics are emitted once "
    "per batch and checkpoints are written every thirty seconds. No customer identifiers "
    "are stored in this layer."
)


def prefill_prompt(sections, nonce):
    needle = max(3, int(sections * 0.9))
    parts = [
        f"Operational handbook. This document has numbered sections. Run nonce: {nonce}. "
        "The answer to the question at the end appears in exactly one section.\n\n"
    ]
    for i in range(1, sections + 1):
        if i == needle:
            parts.append(
                f"Section {i}. DEPLOYMENT CODE: the deployment code for program vulbox/999 "
                "is ZX-4471. This is the only place the code appears.\n\n"
            )
        else:
            parts.append(f"Section {i}. {FILLER}\n\n")
    parts.append(
        "Question: What is the deployment code for program vulbox/999? "
        "Reply with exactly `<code> @ section <number>` and nothing else.\n"
    )
    return "".join(parts), needle


CONFIGS = [
    ("decode-256", 256, None),
    ("decode-1024", 1024, None),
    ("prefill-1k", 128, 16),
    ("prefill-4k", 128, 64),
    ("prefill-16k", 128, 254),
    ("prefill-32k", 128, 508),
]
RUNS = 3


def build(config, sections, nonce):
    if sections is None:
        return DECODE_PROMPT.format(nonce=nonce), None
    return prefill_prompt(sections, nonce)


def aggregate(results):
    out = {}
    for cfg in {r["config"] for r in results}:
        rows = [r for r in results if r["config"] == cfg]
        def stats(key):
            vals = [r[key] for r in rows if isinstance(r.get(key), (int, float))]
            if not vals:
                return None
            return {"median": round(statistics.median(vals), 1), "min": round(min(vals), 1), "max": round(max(vals), 1)}
        out[cfg] = {
            "runs": len(rows),
            "prompt_tokens": rows[0]["prompt_tokens"],
            "completion_tokens": stats("completion_tokens"),
            "cached_tokens": stats("cached_tokens"),
            "ttft_s": stats("ttft_s"),
            "wall_s": stats("wall_s"),
            "prompt_per_second": stats("prompt_per_second"),
            "predicted_per_second": stats("predicted_per_second"),
            "draft_accept_pct": None,
            "gpu_util_max": max((r["gpu_util_max"] or 0) for r in rows),
            "vram_used_max_mib": max((r["vram_used_max_mib"] or 0) for r in rows),
            "power_max_w": max((r["power_max_w"] or 0) for r in rows),
            "temp_max_c": max((r["temp_max_c"] or 0) for r in rows),
            "finish": sorted({r["finish_reason"] for r in rows}),
        }
        drafts = [r for r in rows if r.get("draft_n")]
        if drafts:
            accepted = sum(r["draft_n_accepted"] or 0 for r in drafts)
            total = sum(r["draft_n"] or 0 for r in drafts)
            out[cfg]["draft_accept_pct"] = round(100 * accepted / total, 1) if total else None
    return out


def main():
    open(OUT, "w", encoding="utf-8").close()
    # Warm-up: load the kernels, discard the measurement.
    stream_call("warmup", 0, DECODE_PROMPT.format(nonce=uuid.uuid4().hex[:8]), 32)

    results = []
    for config, max_tokens, sections in CONFIGS:
        for run in range(1, RUNS + 1):
            prompt, needle = build(config, sections, uuid.uuid4().hex[:8])
            result = stream_call(config, run, prompt, max_tokens)
            result["needle_section"] = needle
            results.append(result)

    summary = aggregate(results)
    print(json.dumps(summary, ensure_ascii=False, indent=2))

    summary_path = os.path.join(HERE, "..", "raw", "fair-summary.json")
    with open(summary_path, "w", encoding="utf-8") as fh:
        json.dump({"aggregate": summary, "runs": results}, fh, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
