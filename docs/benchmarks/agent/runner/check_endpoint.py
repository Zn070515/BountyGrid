# -*- coding: utf-8 -*-
"""Check that a local OpenAI-compatible endpoint is up and list models."""
import argparse
import json
import sys
import urllib.request


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    ap.add_argument("--timeout", type=float, default=5.0)
    args = ap.parse_args()
    url = args.base_url.rstrip("/") + "/models"
    try:
        with urllib.request.urlopen(url, timeout=args.timeout) as r:
            data = json.loads(r.read().decode("utf-8"))
    except Exception as e:
        print(f"NOT READY: {url} -> {e}")
        return 1
    models = [m.get("id") for m in (data.get("data") or [])]
    print(f"OK: {url}")
    for m in models:
        print(" -", m)
    if not models:
        print("(no models reported; the server may still accept /chat/completions)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
