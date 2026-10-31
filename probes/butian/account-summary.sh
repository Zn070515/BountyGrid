#!/usr/bin/env bash
# Read-only: Butian account capability + compliance gate presence.
set -euo pipefail
SESSION="${SESSION:-btprobe}"
STATE_FILE="${STATE_FILE:-bounty-butian.json}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/probe.sh"

open_wait "https://www.butian.net/WhiteHat/Center"
ab wait --fn "document.querySelector('iframe') && document.querySelector('iframe').contentDocument && document.querySelector('iframe').contentDocument.body.innerText.length > 200" >/dev/null 2>&1 || true
ab wait 500 >/dev/null 2>&1 || true
U="$(page_url)"
if [[ "$U" == *"user.skyeye.qianxin.com"* || "$U" == "https://www.butian.net" || "$U" == "https://www.butian.net/" ]]; then
  emit_not_logged_in "butian/account-summary" "butian" "$U" "$(page_title)"
  safety_footer
  exit 0
fi

ACCOUNT_JSON="$(eval_js <<'EOF'
(() => {
  const f=document.querySelector('iframe');
  const t=f ? f.contentDocument.body.innerText.replace(/\n{3,}/g,'\n') : document.body.innerText.replace(/\n{3,}/g,'\n');
  const loaded=t.trim().length>50;
  const pick=(re)=>{ const m=t.match(re); return m?m[1].trim():null; };
  return {
    loaded,
    level: loaded?pick(/(实习白帽|初级白帽|中级白帽|高级白帽|精英白帽|安全专家)/):null,
    joined_days: loaded?pick(/已加入补天(\d+)天/):null,
    practical_certification: loaded ? !t.includes('未完成实战认证') : null,
    practical_text: loaded ? (t.includes('未完成实战认证') ? '未完成实战认证' : (t.includes('实战认证') ? '已认证/未知' : null)) : null,
    honor_coins: loaded?pick(/(\d+)\s*\n\s*荣誉币/):null,
    kb_balance: loaded?pick(/(\d+)\s*\n\s*库币余额/):null,
    points: loaded?pick(/(\d+)\s*\n\s*积分/):null,
    bounty_total: loaded?pick(/(\d+)\s*\n\s*已获奖金/):null,
    submitted: loaded?pick(/已提交\s*\n?\s*(\d+)/):null,
    pending_review: loaded?pick(/待审核\s*\n?\s*(\d+)/):null,
    pending_supplement: loaded?pick(/待补充\s*\n?\s*(\d+)/):null,
    rank: loaded?pick(/补天排名\s*\n?\s*([-\d]+)/):null
  };
})()
EOF
)"

# Read-only: detect the quarterly compliance gate on the submit page.
open_wait "https://www.butian.net/Loo/submit"
ab wait 1800 >/dev/null 2>&1 || true
COMPLIANCE_JSON="$(eval_js <<'EOF'
(() => {
  const t=document.body.innerText.replace(/\n{3,}/g,'\n');
  return {
    compliance_gate_present: t.includes('合规考核未完成') || t.includes('本季度合规考核'),
    compliance_gate_text: (t.match(/[^\n]*合规考核[^\n]*/g)||[]).slice(0,3),
    accepted_categories_text: (t.match(/目前接收[^\n]*/g)||[]).slice(0,2),
    submit_form_present: t.includes('漏洞标题') && t.includes('详细细节')
  };
})()
EOF
)"

python -X utf8 -I -c '
import json,sys
acct=json.loads(sys.argv[1])
comp=json.loads(sys.argv[2])
print(json.dumps({
  "probe":"butian/account-summary",
  "account":acct,
  "submission_gate":comp,
  "can_submit_general_hint": not comp.get("compliance_gate_present",False),
  "can_submit_exclusive_hint": (not comp.get("compliance_gate_present",False)) and acct.get("practical_certification",False)
},ensure_ascii=False,indent=2))
' "$ACCOUNT_JSON" "$COMPLIANCE_JSON"
safety_footer
