import json
import os
import subprocess
import threading
import time
import urllib.request

BASE = "http://127.0.0.1:8080"
MODEL = "qwen3.8-flash-next-iq3_s"
OUT = os.environ.get("STRATA_BENCH_OUT") or os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "raw", "2026-11-01-iq3s.jsonl"
)

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

        def av(v):
            return round(sum(v) / len(v), 1) if v else None

        return {
            "gpu_util_max": mx(self.gpu_util),
            "gpu_util_avg": av(self.gpu_util),
            "vram_used_max_mib": mx(self.vram),
            "power_max_w": mx(self.power),
            "temp_max_c": mx(self.temp),
            "cpu_pct_max": mx(self.cpu),
            "cpu_pct_avg": av(self.cpu),
            "ram_pct_max": mx(self.ram_pct),
        }


def call(name, prompt, max_tokens):
    body = json.dumps({
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "stream": False,
    }).encode("utf-8")
    req = urllib.request.Request(
        BASE + "/v1/chat/completions",
        data=body,
        headers={"Content-Type": "application/json", "Authorization": "Bearer strata-local"},
    )
    sampler = Sampler()
    sampler.start()
    t0 = time.perf_counter()
    with opener.open(req, timeout=1800) as response:
        raw = response.read()
    t1 = time.perf_counter()
    sampler.stop.set()
    sampler.join(timeout=5)

    data = json.loads(raw)
    choice = (data.get("choices") or [{}])[0]
    message = choice.get("message") or {}
    usage = data.get("usage") or {}
    elapsed = t1 - t0
    prompt_tokens = usage.get("prompt_tokens", 0)
    completion_tokens = usage.get("completion_tokens", 0)
    result = {
        "test": name,
        "prompt_chars": len(prompt),
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": usage.get("total_tokens", prompt_tokens + completion_tokens),
        "wall_s": round(elapsed, 2),
        "decode_tok_per_s": round(completion_tokens / elapsed, 1) if elapsed > 0 else None,
        "finish_reason": choice.get("finish_reason"),
        "content_chars": len(message.get("content") or ""),
        "reasoning_chars": len(message.get("reasoning_content") or ""),
        "content_head": (message.get("content") or "")[:800],
        "reasoning_head": (message.get("reasoning_content") or "")[:300],
        **sampler.summary(),
    }
    with open(OUT, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(result, ensure_ascii=False) + "\n")
    return result


SHORT_PROMPT = (
    "Write a technical explanation of how HTTP/2 multiplexing interacts with TCP "
    "head-of-line blocking, and how HTTP/3 and QUIC address the remaining problem. "
    "Target about 900 words. Use concrete protocol details: frame types, stream "
    "identifiers, flow-control windows, and the relevant QUIC packet and stream mechanics."
)

GO_CODE = r'''package cache

import (
	"context"
	"sync"
	"time"
)

type entry struct {
	value   []byte
	expires time.Time
}

type Cache struct {
	mu    sync.Mutex
	items map[string]entry
	stop  chan struct{}
}

func NewCache(ctx context.Context, ttl time.Duration) *Cache {
	c := &Cache{items: make(map[string]entry), stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-time.After(time.Second):
				c.evict()
			}
		}
	}()
	return c
}

func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = entry{value: value, expires: time.Now().Add(ttl)}
}

func (c *Cache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.items {
		if now.After(e.expires) {
			delete(c.items, k)
		}
	}
}

func (c *Cache) Snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, 0)
	for _, e := range c.items {
		out = append(out, e.value...)
	}
	return out
}

func (c *Cache) KeysWithPrefix(prefix string) []string {
	var keys []string
	for k := range c.items {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (c *Cache) Close() error {
	close(c.stop)
	return nil
}

type Counter struct {
	mu sync.Mutex
	n  int
}

func (c Counter) Add(delta int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n += delta
	return c.n
}

func (c *Cache) Warm(keys []string) {
	var wg sync.WaitGroup
	for _, k := range keys {
		go func() {
			wg.Add(1)
			defer wg.Done()
			if _, ok := c.Get(k); !ok {
				c.Set(k, []byte("warm"), time.Minute)
			}
		}()
	}
	wg.Wait()
}
'''

CODE_PROMPT = (
    "Review the following Go file. List every concurrency bug and correctness bug. "
    "For each bug give: the function name, the exact reason it is wrong, and the minimal fix. "
    "Do not rewrite the whole file.\n\n" + GO_CODE
)


def build_long_prompt():
    filler = (
        "This section records routine operational detail for the logging pipeline. "
        "The pipeline batches records, validates schema versions, applies retention rules, "
        "and retries transient failures with exponential backoff. Metrics are emitted once "
        "per batch and checkpoints are written every thirty seconds. No customer identifiers "
        "are stored in this layer."
    )
    parts = [
        "Operational handbook. This document has numbered sections. The answer to the "
        "question at the end appears in exactly one section.\n\n"
    ]
    for i in range(1, 201):
        if i == 137:
            parts.append(
                f"Section {i}. DEPLOYMENT CODE: the deployment code for program vulbox/999 "
                "is ZX-4471. This is the only place the code appears.\n\n"
            )
        else:
            parts.append(f"Section {i}. {filler}\n\n")
    parts.append(
        "Question: What is the deployment code for program vulbox/999? "
        "Reply with exactly `<code> @ section <number>` and nothing else.\n"
    )
    return "".join(parts)


def main():
    open(OUT, "w", encoding="utf-8").close()
    results = []
    results.append(call("short-generation", SHORT_PROMPT, 2048))
    results.append(call("code-review", CODE_PROMPT, 2048))
    results.append(call("long-context-16k", build_long_prompt(), 256))

    print(json.dumps(results, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
