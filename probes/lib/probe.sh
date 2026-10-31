#!/usr/bin/env bash
# BountyGrid read-only probe helpers.
# No platform mutation: no signup, no NDA, no submission, no payout.
set -euo pipefail

SESSION="${SESSION:-bounty}"
STATE="${STATE:-}"

default_state_dir() {
  if command -v cygpath >/dev/null 2>&1; then
    printf '%s/.agent-browser/state' "$(cygpath -w "$HOME")"
  else
    printf '%s/.agent-browser/state' "$HOME"
  fi
}
STATE_DIR="${STATE_DIR:-$(default_state_dir)}"
STATE_FILE="${STATE_FILE:-}"
if [ -z "$STATE" ] && [ -n "$STATE_FILE" ]; then
  STATE="$STATE_DIR/$STATE_FILE"
fi

AB=(agent-browser --session "$SESSION")

ab() { "${AB[@]}" "$@"; }

open_wait() {
  local url="$1"
  if [ -n "$STATE" ]; then
    # Restore saved cookies/localStorage for a dedicated probe session.
    ab --state "$STATE" open "$url" >/dev/null 2>&1 || true
  else
    ab open "$url" >/dev/null 2>&1 || true
  fi
  ab wait --load networkidle >/dev/null 2>&1 || true
  ab wait 1200 >/dev/null 2>&1 || true
}

page_url()   { ab get url 2>/dev/null | head -1; }
page_title() { ab get title 2>/dev/null | head -1; }

# Usage: eval_js <<'EOF' ... EOF
eval_js() { ab eval --stdin; }

# Normalize a browser eval result (JSON string) into a JSON object/array.
# Usage: echo "$raw" | normalize_json
normalize_json() {
  python -X utf8 -I -c '
import json,sys
s=sys.stdin.read().strip()
try:
    v=json.loads(s)
    if isinstance(v,str):
        v=json.loads(v)
    print(json.dumps(v,ensure_ascii=False,indent=2))
except Exception as e:
    print(json.dumps({"error":str(e),"raw":s[:500]},ensure_ascii=False))
'
}

emit_not_logged_in() {
  local probe="$1" platform="$2" url="$3" title="$4"
  python -X utf8 -I -c '
import json,sys
print(json.dumps({
  "probe":sys.argv[1],
  "platform":sys.argv[2],
  "logged_in":False,
  "url":sys.argv[3],
  "title":sys.argv[4],
  "note":"session expired or login required; probe stopped read-only"
},ensure_ascii=False,indent=2))
' "$probe" "$platform" "$url" "$title"
}

safety_footer() {
  echo "SAFETY: read-only; no signup/NDA/submission/payout"
}
