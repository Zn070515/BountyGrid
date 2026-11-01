"""Summarize the JSON files produced by the ollama_smoke suites.

    python -I summarize.py <output-dir>
"""

import json
import sys
from pathlib import Path

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")


def load(directory, name):
    path = Path(directory) / f"{name}.json"
    if not path.exists():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def main():
    directory = sys.argv[1] if len(sys.argv) > 1 else "."
    proposal = load(directory, "proposal")
    if proposal:
        print("== proposal ==")
        print(
            f"context={proposal['context_chars']} chars / est {proposal['context_tokens']} tok, "
            f"actual {proposal['prompt_tokens']} tok, {proposal['tok_per_s']} tok/s, "
            f"parse={proposal['json_parse_ok']}, ranked={proposal['ranked_count']}, "
            f"chosen={proposal.get('chosen', {}).get('action')}"
        )

    filters = load(directory, "filters")
    if filters:
        print("== filters ==")
        for r in filters:
            print(
                f"{r['case']}: parse={r['json_parse_ok']} proposals={r['proposal_count']} "
                f"ranked={r['ranked_count']} chosen={r.get('chosen')} "
                f"errors={r.get('validate_errors', [])}"
            )

    injection = load(directory, "injection")
    if injection:
        print("== injection ==")
        print(
            f"escaped={injection['injection_escaped']} raw_close_markers={injection['raw_close_markers']} "
            f"model_action={injection.get('model_action')} action_hint={injection.get('action_hint')} "
            f"ranked={injection['ranked_count']} chosen={injection.get('chosen')}"
        )

    budget = load(directory, "budget")
    if budget:
        print("== budget ==")
        for r in budget:
            if "error" in r:
                print(f"max_chars={r['max_chars']}: error={r['error']}")
                continue
            print(
                f"max_chars={r['max_chars']}: context={r['context_chars']} "
                f"est={r['estimated_tokens']} actual={r['actual_prompt_tokens']} "
                f"est/actual={r.get('estimate_over_actual')} truncated={r['truncated']} "
                f"ranked={r['ranked_count']}"
            )

    repeat = load(directory, "repeat")
    if repeat:
        print("== repeat ==")
        print(
            f"n={repeat['n']} parse_ok={repeat['parse_ok']} ranked_ok={repeat['ranked_ok']} "
            f"valid_action={repeat['valid_action']} unique_outputs={repeat['unique_outputs']}"
        )

    loop = load(directory, "loop")
    if loop:
        print("== loop ==")
        print(
            f"stop={loop['stop']} steps={loop['steps']} proposer_calls={loop['proposer_calls']} "
            f"parse_failures={loop['parse_failures']} stall_action={loop['stall_action']} "
            f"last={loop['last_detection']['verdict']}/{loop['last_detection']['action']} "
            f"{loop['last_detection']['reasons']}"
        )
        for step in loop["step_details"]:
            print(
                f"  step {step['step']}: {step['action']} {step['verdict']} "
                f"{step['detector_action']} {step['reasons']}"
            )


if __name__ == "__main__":
    main()
