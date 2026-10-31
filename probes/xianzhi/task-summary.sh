#!/usr/bin/env bash
# Read-only: Xianzhi task list summary (91 tasks, 5 pages).
set -euo pipefail
SESSION="${SESSION:-xzprobe}"
STATE_FILE="${STATE_FILE:-bounty-xianzhi.json}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/probe.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

open_wait "https://yundun.console.aliyun.com/?p=xznew#/taskmanagement/tasks/all"
if [[ "$(page_url)" == *"account.aliyun.com/login"* ]]; then
  emit_not_logged_in "xianzhi/task-summary" "xianzhi" "$(page_url)" "$(page_title)"
  safety_footer
  exit 0
fi

for pg in 1 2 3 4 5; do
  if [ "$pg" != "1" ]; then
    eval_js >/dev/null <<EOF || true
(() => { const b=[...document.querySelectorAll('.cs-pagination-item')].find(x=>x.innerText.trim()==='$pg'); if(b){b.click(); return 'ok';} return 'nf'; })()
EOF
    ab wait 500 >/dev/null 2>&1 || true
    # Wait until the new page's cards are rendered (avoids capturing a loading state).
    ab wait --fn "document.querySelectorAll('.card-item').length > 0" >/dev/null 2>&1 || true
    ab wait 900 >/dev/null 2>&1 || true
  else
    ab wait --fn "document.querySelectorAll('.card-item').length > 0" >/dev/null 2>&1 || true
    ab wait 500 >/dev/null 2>&1 || true
  fi
  eval_js > "$TMP/page-$pg.json" <<'EOF'
(() => {
  const cards=[...document.querySelectorAll('.card-item')].map(c=>({
    title: ((c.querySelector('.card-item-title')||{}).innerText||'').trim(),
    text: c.innerText.replace(/\n+/g,'|'),
    buttons: [...c.querySelectorAll('button')].map(b=>b.innerText.trim())
  }));
  return JSON.stringify({cards});
})()
EOF
done

python -X utf8 -I - "$TMP" <<'PY'
import json,glob,re,sys,os
tmp=sys.argv[1]
cards=[]
for f in sorted(glob.glob(os.path.join(tmp,'page-*.json'))):
    raw=open(f,encoding='utf-8').read().strip()
    try:
        v=json.loads(raw)
        if isinstance(v,str): v=json.loads(v)
        cards.extend(v.get('cards',[]))
    except Exception as e:
        cards.append({'title':'__PARSE_ERROR__','error':str(e),'raw':raw[:200]})

def g(t,k):
    m=re.search(k+r'：\|([^|]+)',t)
    return m.group(1).strip() if m else ''

def state(title):
    if '任务结束' in title: return '任务结束'
    if '任务暂停' in title: return '任务暂停'
    return '进行中'

by_type={}
by_state={}
active=[]
for c in cards:
    t=c.get('text','')
    typ=g(t,'任务类型') or 'UNKNOWN'
    st=state(c.get('title',''))
    by_type[typ]=by_type.get(typ,0)+1
    by_state[st]=by_state.get(st,0)+1
    if st=='进行中':
        active.append({
            'title':c.get('title','').split('\n')[0],
            'type':typ,
            'bounty':g(t,'赏金范围'),
            'limit':g(t,'人数限制'),
            'signup':g(t,'报名时间')
        })

print(json.dumps({
    'probe':'xianzhi/task-summary',
    'total':len(cards),
    'by_type':by_type,
    'by_state':by_state,
    'active_count':len(active),
    'active':active[:40],
    'notes':[
        'Buttons are not eligibility; ended tasks may still show 我要报名.',
        'Scope requires signup; unregistered scope is GATED, not empty.'
    ]
},ensure_ascii=False,indent=2))
PY
safety_footer
