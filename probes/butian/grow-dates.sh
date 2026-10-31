#!/usr/bin/env bash
# Read-only regression probe: Butian GROW dates.
# Rule source: AI track rules modal (registration/activity).
# Phase source: activity center listing (monthly phase 10/01-10/31).
# Expected: registration 2026-08-25~2026-11-30; activity 2026-09-01~2026-11-30.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/probe.sh"

open_wait "https://www.butian.net/Grow"
eval_js >/dev/null <<'EOF' || true
(() => {
  const b=[...document.querySelectorAll('button')].find(x=>x.innerText.trim()==='查看规则');
  if(b){b.click(); return 'ok';} return 'nf';
})()
EOF
ab wait 1000 >/dev/null 2>&1 || true

RULES_JSON="$(eval_js <<'EOF'
(() => {
  const t=document.body.innerText.replace(/\n{3,}/g,'\n');
  const pick=(re)=>{ const m=t.match(re); return m?m[1].trim():null; };
  return JSON.stringify({
    source_url: location.href,
    registration_text: pick(/报名时间：\s*([0-9.~\-]+)/),
    activity_text: pick(/活动时间：\s*([0-9.~\-]+)/),
    has_ai_rule: t.includes('AI 辅助漏洞挖掘规则')
  });
})()
EOF
)"

open_wait "https://www.butian.net/Active/center"
PHASE_JSON="$(eval_js <<'EOF'
(() => {
  const t=document.body.innerText.replace(/\n{3,}/g,'\n');
  const i=t.indexOf('2026GROW计划漏洞挖掘实战-第二期');
  const chunk=i>=0?t.slice(i,i+260):'';
  const m=chunk.match(/2026\/\d{2}\/\d{2}\s*-\s*2026\/\d{2}\/\d{2}/);
  return JSON.stringify({
    found: i>=0,
    chunk: chunk.replace(/\n/g,' | '),
    phase2_text: m?m[0]:null
  });
})()
EOF
)"

python -X utf8 -I -c '
import json,sys
def lm(s):
    v=json.loads(s)
    if isinstance(v,str): v=json.loads(v)
    return v
rules=lm(sys.argv[1]); phase=lm(sys.argv[2])
reg=rules.get("registration_text"); act=rules.get("activity_text")
print(json.dumps({
  "probe":"butian/grow-dates",
  "rules":rules,
  "activity_center_phase":phase,
  "checks":{
    "registration_ok": reg=="2026.8.25~2026.11.30",
    "activity_ok": act=="2026.9.1~2026.11.30",
    "phase2_is_monthly_only": bool(phase.get("phase2_text")),
    "overall_end":"2026-11-30"
  },
  "note":"漏洞挖掘实战第二期 2026-10-01~10-31 是月度阶段，不是整个 GROW 的结束时间。"
},ensure_ascii=False,indent=2))
' "$RULES_JSON" "$PHASE_JSON"
safety_footer
