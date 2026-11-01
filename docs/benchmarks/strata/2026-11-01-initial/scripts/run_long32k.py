import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import run_test as rt


def build_long_prompt(sections):
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
    for i in range(1, sections + 1):
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


result = rt.call("long-context-32k", build_long_prompt(500), 256)
print(json.dumps(result, ensure_ascii=False, indent=2))
