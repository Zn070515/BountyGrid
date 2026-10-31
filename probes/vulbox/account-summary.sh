#!/usr/bin/env bash
# Read-only: Vulbox account level/reputation/season + joined projects.
set -euo pipefail
SESSION="${SESSION:-vbprobe}"
STATE_FILE="${STATE_FILE:-bounty-vulbox.json}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/probe.sh"

open_wait "https://user.vulbox.com/home"
if [[ "$(page_url)" != *"user.vulbox.com"* ]]; then
  emit_not_logged_in "vulbox/account-summary" "vulbox" "$(page_url)" "$(page_title)"
  safety_footer
  exit 0
fi

HOME_JSON="$(eval_js <<'EOF'
(() => {
  const t=document.body.innerText.replace(/\n{3,}/g,'\n');
  const pick=(re)=>{ const m=t.match(re); return m?m[1].trim():null; };
  return {
    reputation_text: pick(/信誉(优秀|良好|较差)\s*\|\s*(\d+)\s*分/),
    reputation_score: pick(/信誉(?:优秀|良好|较差)\s*\|\s*(\d+)\s*分/),
    level: pick(/当前等级:\s*\n?\s*([^\n]+)/),
    exp: pick(/经验值:\s*\n?\s*(\d+)/),
    next_level: pick(/下一等级:\s*\n?\s*([^\n]+)/),
    next_exp: (() => { const m=t.match(/下一等级:[\s\S]{0,80}?经验值:\s*\n?\s*(\d+)/); return m?m[1]:null; })(),
    season: pick(/赛季战况\s*\n?\s*(S\d+)/),
    season_points: pick(/赛季积分\s*\n?\s*(\d+)/),
    season_rank: pick(/赛季排名\s*\n?\s*([0-9+]+)/),
    balance: pick(/账户余额\s*\n?\s*([\d.]+)\s*元/),
    commission: pick(/提成金\s*\n?\s*([\d.]+)\s*元/),
    coupons: pick(/我的券\s*\n?\s*(\d+)\s*张/),
    pending_review: pick(/待审阅\s*\n?\s*(\d+)/),
    processing: pick(/处理中\s*\n?\s*(\d+)/),
    paid: pick(/已打款\s*\n?\s*(\d+)/)
  };
})()
EOF
)"

open_wait "https://user.vulbox.com/projectHall/myProject"
JOINED_JSON="$(eval_js <<'EOF'
(() => {
  const t=document.body.innerText.replace(/\n{3,}/g,'\n');
  const names=[...document.querySelectorAll('h1,h2,h3,h4,.project-name,.title')]
    .map(x=>x.innerText.trim()).filter(x=>x && x.includes('安全应急响应中心'));
  return {
    joined_marker_count: (t.match(/已加入/g)||[]).length,
    joined_project_names: [...new Set(names)].slice(0,20),
    page_text_has_two: t.includes('共 2 条数据')
  };
})()
EOF
)"

python -X utf8 -I -c '
import json,sys
home=json.loads(sys.argv[1]); joined=json.loads(sys.argv[2])
print(json.dumps({
  "probe":"vulbox/account-summary",
  "account":home,
  "joined_projects":joined
},ensure_ascii=False,indent=2))
' "$HOME_JSON" "$JOINED_JSON"
safety_footer
