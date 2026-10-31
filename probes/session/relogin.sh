#!/usr/bin/env bash
# Read-only helper: open a platform login page in a dedicated probe session,
# wait for the human to log in, then save the session state.
# It never fills credentials, never checks agreements, never clicks login.
#
# Usage: bash probes/session/relogin.sh vulbox|xianzhi|butian
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/probe.sh"

PLATFORM="${1:-}"

case "$PLATFORM" in
  vulbox)
    SESSION="vbprobe"; STATE="$STATE_DIR/bounty-vulbox.json"
    URL="https://www.vulbox.com/account/login"
    ;;
  xianzhi)
    SESSION="xzprobe"; STATE="$STATE_DIR/bounty-xianzhi.json"
    URL="https://yundun.console.aliyun.com/?p=xznew#/taskmanagement/tasks/all"
    ;;
  butian)
    SESSION="btprobe"; STATE="$STATE_DIR/bounty-butian.json"
    URL="https://www.butian.net/WhiteHat/Center"
    ;;
  *)
    echo "usage: $0 vulbox|xianzhi|butian" >&2
    exit 2
    ;;
esac

echo "Opening $PLATFORM login in session '$SESSION' (headed)."
echo "Please log in manually. Do NOT ask the probe to accept agreements."
# Deliberately start without --state: a stale/contaminated state file can make
# the browser hang on navigation. A successful login overwrites it below.
agent-browser --session "$SESSION" --headed open "$URL" >/dev/null 2>&1 || true

ok=0
for _ in $(seq 1 200); do
  u="$(agent-browser --session "$SESSION" get url 2>/dev/null | head -1 || true)"
  case "$PLATFORM" in
    vulbox)  [[ "$u" == *"user.vulbox.com"* ]] && ok=1 ;;
    xianzhi) [[ "$u" == *"yundun.console.aliyun.com"* || "$u" == *"xianzhi.aliyun.com"* ]] && [[ "$u" != *"account.aliyun.com/login"* ]] && ok=1 ;;
    butian)  [[ "$u" == *"butian.net/WhiteHat/Center"* ]] && ok=1 ;;
  esac
  [ "$ok" = "1" ] && break
  sleep 3
done

if [ "$ok" = "1" ]; then
  agent-browser --session "$SESSION" state save "$STATE" >/dev/null 2>&1 || true
  echo "LOGIN_OK platform=$PLATFORM session=$SESSION state_saved=true"
else
  echo "LOGIN_TIMEOUT platform=$PLATFORM session=$SESSION state_saved=false" >&2
  exit 1
fi

safety_footer
