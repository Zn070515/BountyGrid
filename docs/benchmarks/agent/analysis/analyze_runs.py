# -*- coding: utf-8 -*-
"""Analyze benchmark runs: per-arm success, paired McNemar, bootstrap CI, cost."""
import argparse
import json
import math
import random
import statistics
from collections import Counter, defaultdict
from pathlib import Path

P_HIT, P_MISS, P_OUT = 0.0207, 0.827, 8.265


def load(path):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def cost_of(rec):
    u = (rec.get("agent") or {}).get("usage") or {}
    if "cost_cny" in u:
        return float(u["cost_cny"])
    hit = u.get("cache_hit_tokens", 0) or 0
    miss = u.get("cache_miss_tokens", 0) or 0
    out = u.get("output_tokens", 0) or 0
    return hit / 1e6 * P_HIT + miss / 1e6 * P_MISS + out / 1e6 * P_OUT


def chi2_sf_1df(x):
    return math.erfc(math.sqrt(max(x, 0) / 2))


def mcnemar(pairs):
    """pairs: list of (a_ok, b_ok). Returns (b_count, c_count, p)."""
    b = sum(1 for a, bb in pairs if a and not bb)
    c = sum(1 for a, bb in pairs if bb and not a)
    n = b + c
    if n == 0:
        return b, c, 1.0
    stat = (abs(b - c) - 1) ** 2 / n
    return b, c, chi2_sf_1df(stat)


def bootstrap_diff(vals_a, vals_b, n=5000, seed=7):
    """Paired bootstrap of mean difference (a - b) over tasks."""
    rnd = random.Random(seed)
    diffs = []
    m = len(vals_a)
    for _ in range(n):
        idx = [rnd.randrange(m) for _ in range(m)]
        diffs.append(sum(vals_a[i] - vals_b[i] for i in idx) / m)
    diffs.sort()
    return diffs[int(n * 0.025)], diffs[int(n * 0.975)]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("runs", help="run.jsonl")
    ap.add_argument("--markdown", action="store_true")
    args = ap.parse_args()
    rows = load(args.runs)
    if not rows:
        raise SystemExit("no runs")

    by_arm = defaultdict(list)
    for r in rows:
        by_arm[r["arm"]].append(r)

    print("== per arm ==")
    for arm, rs in sorted(by_arm.items()):
        succ = [1 if r["success"] else 0 for r in rs]
        costs = [cost_of(r) for r in rs if (r.get("agent") or {}).get("usage")]
        line = f"{arm:10s} n={len(rs):3d} success={sum(succ)}/{len(rs)} ({sum(succ)/len(rs)*100:.1f}%)"
        if costs:
            line += f" cost_mean={statistics.mean(costs):.3f} cost_median={statistics.median(costs):.3f}"
        print(line)

    # paired by task: average repeats first
    task_arm = defaultdict(lambda: defaultdict(list))
    for r in rows:
        task_arm[r["task_id"]][r["arm"]].append(1 if r["success"] else 0)
    arms = sorted(by_arm)
    print("\n== paired comparisons ==")
    for i in range(len(arms)):
        for j in range(i + 1, len(arms)):
            a, b = arms[i], arms[j]
            pairs, va, vb = [], [], []
            for task, d in task_arm.items():
                if a in d and b in d:
                    pa = statistics.mean(d[a])
                    pb = statistics.mean(d[b])
                    va.append(pa)
                    vb.append(pb)
                    pairs.append((pa >= 0.5, pb >= 0.5))
            if not pairs:
                continue
            bb, cc, p = mcnemar(pairs)
            lo, hi = bootstrap_diff(va, vb)
            print(f"{a} vs {b}: n_tasks={len(pairs)} discordant b={bb} c={cc} "
                  f"mcnemar_p={p:.4f} diff={statistics.mean(va)-statistics.mean(vb):+.3f} "
                  f"CI95=[{lo:+.3f},{hi:+.3f}]")

    print("\n== per task ==")
    for task, d in sorted(task_arm.items()):
        parts = " ".join(f"{arm}={statistics.mean(v):.2f}" for arm, v in sorted(d.items()))
        print(f"{task:28s} {parts}")

    if args.markdown:
        print("\n| arm | n | success |")
        print("|---|---:|---:|")
        for arm, rs in sorted(by_arm.items()):
            print(f"| {arm} | {len(rs)} | {sum(1 for r in rs if r['success'])}/{len(rs)} |")


if __name__ == "__main__":
    main()
