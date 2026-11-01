"""Versioned local-model evaluation runner for the Ollama smoke suites.

This is a reproducible smoke-level dataset, not a benchmark: each run records
the model, Ollama/Go versions, suite configuration, raw JSON and a summary.

    python -I probes/ollama_smoke/run_eval.py --model qwen25-0.5b-local
    python -I probes/ollama_smoke/run_eval.py --model qwen25-0.5b-local --runs 3

Raw output defaults to probes/out/ollama-eval/<timestamp>/ (gitignored).
"""

import argparse
import hashlib
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")

SUITES = ["proposal", "filters", "injection", "budget", "repeat", "loop"]


def run(cmd, cwd, timeout):
    return subprocess.run(
        cmd, cwd=cwd, capture_output=True, text=True,
        encoding="utf-8", errors="replace", timeout=timeout,
    )


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None


def summarize(out_dir):
    stats = {suite: {"runs": 0, "errors": 0} for suite in SUITES}
    result_files = sorted(out_dir.glob("*.json"))
    for path in result_files:
        suite = path.stem.split("-")[0]
        if suite not in stats:
            continue
        data = load(path)
        if data is None:
            stats[suite]["errors"] += 1
            continue
        stats[suite]["runs"] += 1
        if suite == "proposal":
            stats[suite].setdefault("parse_ok", 0)
            stats[suite].setdefault("ranked_ok", 0)
            stats[suite].setdefault("tok_per_s", [])
            stats[suite]["parse_ok"] += int(bool(data.get("json_parse_ok")))
            stats[suite]["ranked_ok"] += int(data.get("ranked_count", 0) > 0)
            if data.get("tok_per_s"):
                stats[suite]["tok_per_s"].append(data["tok_per_s"])
        elif suite == "filters":
            stats[suite].setdefault("cases", {})
            stats[suite].setdefault("passes", 0)
            stats[suite].setdefault("case_runs", 0)
            ok = True
            for case in data:
                name = case["case"]
                bucket = stats[suite]["cases"].setdefault(
                    name, {"runs": 0, "ranked": 0, "parse_ok": 0})
                bucket["runs"] += 1
                bucket["ranked"] += int(case.get("ranked_count", 0) > 0)
                bucket["parse_ok"] += int(bool(case.get("json_parse_ok")))
                stats[suite]["case_runs"] += 1
                expected = name == "valid-read-scope"
                if bool(case.get("ranked_count", 0) > 0) != expected:
                    ok = False
            stats[suite]["passes"] += int(ok)
        elif suite == "injection":
            stats[suite].setdefault("escaped", 0)
            stats[suite].setdefault("ranked", 0)
            stats[suite]["escaped"] += int(bool(data.get("injection_escaped")))
            stats[suite]["ranked"] += int(data.get("ranked_count", 0) > 0)
        elif suite == "budget":
            stats[suite].setdefault("ratios", [])
            for row in data:
                if "estimate_over_actual" in row:
                    stats[suite]["ratios"].append(row["estimate_over_actual"])
        elif suite == "repeat":
            stats[suite].setdefault("runs_reported", 0)
            stats[suite].setdefault("parse_ok", 0)
            stats[suite].setdefault("ranked_ok", 0)
            stats[suite]["runs_reported"] += data.get("n", 0)
            stats[suite]["parse_ok"] += data.get("parse_ok", 0)
            stats[suite]["ranked_ok"] += data.get("ranked_ok", 0)
        elif suite == "loop":
            stats[suite].setdefault("stops", {})
            stop = data.get("stop", "UNKNOWN")
            stats[suite]["stops"][stop] = stats[suite]["stops"].get(stop, 0) + 1
    for suite in stats:
        values = stats[suite].get("tok_per_s")
        if values:
            stats[suite]["tok_per_s_avg"] = round(sum(values) / len(values), 1)
            del stats[suite]["tok_per_s"]
        ratios = stats[suite].get("ratios")
        if ratios:
            stats[suite]["estimate_over_actual_avg"] = round(sum(ratios) / len(ratios), 2)
            del stats[suite]["ratios"]
    return stats


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", default="qwen25-0.5b-local")
    parser.add_argument("--runs", type=int, default=1, help="external runs per suite")
    parser.add_argument("--repeat-n", type=int, default=5, help="temperature-0 repeats inside the repeat suite")
    parser.add_argument("--out", default="", help="output directory (default probes/out/ollama-eval/<timestamp>)")
    parser.add_argument("--repo", default=".", help="repository root")
    parser.add_argument("--timeout", type=int, default=900)
    args = parser.parse_args()

    repo = Path(args.repo).resolve()
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    out_dir = Path(args.out) if args.out else repo / "probes" / "out" / "ollama-eval" / stamp
    out_dir.mkdir(parents=True, exist_ok=True)

    go_version = run(["go", "version"], repo, 30)
    ollama_version = run(["ollama", "--version"], repo, 30)
    ollama_show = run(["ollama", "show", args.model], repo, 60)
    manifest = {
        "started_at": stamp,
        "model": args.model,
        "runs_per_suite": args.runs,
        "repeat_n": args.repeat_n,
        "suites": SUITES,
        "go_version": go_version.stdout.strip(),
        "ollama_version": ollama_version.stdout.strip(),
        "ollama_show": ollama_show.stdout.strip(),
        "note": "smoke-level dataset, not a benchmark; target-model comparison is still required",
    }

    for suite in SUITES:
        for i in range(args.runs):
            cmd = ["go", "run", "./probes/ollama_smoke", "-model", args.model, "-suite", suite]
            if suite == "repeat":
                cmd += ["-n", str(args.repeat_n)]
            try:
                proc = run(cmd, repo, args.timeout)
            except subprocess.TimeoutExpired:
                proc = None
            raw = out_dir / f"{suite}-{i + 1}.json"
            err = out_dir / f"{suite}-{i + 1}.stderr"
            if proc is None:
                raw.write_text("", encoding="utf-8")
                err.write_text("timeout\n", encoding="utf-8")
            else:
                raw.write_text(proc.stdout, encoding="utf-8")
                err.write_text(proc.stderr, encoding="utf-8")
            manifest.setdefault("result_sha256", {})[raw.name] = sha256(raw)

    manifest["finished_at"] = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    (out_dir / "manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    stats = summarize(out_dir)
    (out_dir / "summary.json").write_text(
        json.dumps(stats, ensure_ascii=False, indent=2), encoding="utf-8")

    print(f"model={args.model} runs={args.runs} repeat_n={args.repeat_n}")
    for suite in SUITES:
        print(f"{suite}: {json.dumps(stats[suite], ensure_ascii=False)}")
    print(f"dataset={out_dir}")


if __name__ == "__main__":
    main()
