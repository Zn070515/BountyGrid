# -*- coding: utf-8 -*-
"""Data-free call probe executed inside the acceptance container.

It reads and validates the whole input before importing any target module, so
agent code cannot consume or alter the case stream on first import.
"""
import importlib
import json
import sys

RESULT_PREFIX = "__PROBE_RESULT__"


def main():
    dumps = json.dumps
    out = sys.stdout
    try:
        cases = json.loads(sys.stdin.read())
        if not isinstance(cases, list):
            raise ValueError("probe input must be a list")
        for case in cases:
            if not isinstance(case, dict) or {"module", "func", "args"} - set(case):
                raise ValueError("probe case needs module/func/args")
    except Exception as exc:
        out.write(f"{RESULT_PREFIX}{dumps({'ok': False, 'error': f'input: {exc}'})}\n")
        out.flush()
        return 2

    results = []
    for case in cases:
        try:
            module = importlib.import_module(case["module"])
            func = getattr(module, case["func"])
            results.append({"ok": True, "value": func(*case["args"])})
        except Exception as exc:
            results.append({"ok": False, "error": f"{type(exc).__name__}: {exc}"})

    try:
        payload = dumps(results)
    except TypeError as exc:
        payload = dumps({"ok": False, "error": f"not json serializable: {exc}"})
    out.write(RESULT_PREFIX + payload + "\n")
    out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
