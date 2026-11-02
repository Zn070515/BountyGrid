# -*- coding: utf-8 -*-
"""Host-side hidden checks: inputs cross into the container, expectations do not."""
import json

RESULT_PREFIX = "__PROBE_RESULT__"


def build_probe_input(cases):
    """Serialize only the call inputs; ``expect`` never leaves the host."""
    payload = [
        {"module": case["module"], "func": case["func"], "args": case["args"]}
        for case in cases
    ]
    return json.dumps(payload)


def parse_probe_output(stdout):
    """Return the marked result list, or None when it is missing or malformed."""
    for line in reversed((stdout or "").splitlines()):
        if line.startswith(RESULT_PREFIX):
            try:
                data = json.loads(line[len(RESULT_PREFIX):])
            except json.JSONDecodeError:
                return None
            return data if isinstance(data, list) else None
    return None


def compare_hidden(probe, cases):
    """Compare a probe result with host-side expectations.

    Returns ``{"declared", "pass", "matched", "total", "detail"}``.
    ``detail == "probe_error"`` means the probe crashed or emitted no marked
    line; callers must not treat that as a model mismatch.
    """
    declared = bool(cases)
    if not declared:
        return {"declared": False, "pass": None, "matched": 0, "total": 0,
                "detail": "no cases"}
    results = parse_probe_output(getattr(probe, "stdout", "") or "")
    if results is None or len(results) != len(cases):
        return {"declared": True, "pass": False, "matched": 0, "total": len(cases),
                "detail": "probe_error"}
    matched = 0
    for case, got in zip(cases, results):
        if isinstance(got, dict) and got.get("ok") is True and got.get("value") == case["expect"]:
            matched += 1
    return {
        "declared": True,
        "pass": matched == len(cases),
        "matched": matched,
        "total": len(cases),
        "detail": f"{matched}/{len(cases)} cases",
    }
