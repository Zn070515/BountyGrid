#!/usr/bin/env bash
# Read-only: check login state for the three platforms.
# Each platform uses its own session + saved state (login credentials).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/probe.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

check() {
  local platform="$1" url="$2" sess="$3" state="$4"
  agent-browser --session "$sess" --state "$state" open "$url" >/dev/null 2>&1 || true
  agent-browser --session "$sess" wait --load networkidle >/dev/null 2>&1 || true
  agent-browser --session "$sess" wait 1200 >/dev/null 2>&1 || true
  agent-browser --session "$sess" eval --stdin > "$TMP/$platform.json" <<EOF
(() => {
  const platform="$platform";
  const t=document.body.innerText||'';
  let it='';
  try { const f=document.querySelector('iframe'); if(f&&f.contentDocument) it=f.contentDocument.body.innerText||''; } catch(e){}
  const all=t+'\n'+it;
  let logged=false, reason='';

  if (platform==='xianzhi') {
    const onLogin = location.hostname==='account.aliyun.com' || all.includes('阿里云登录页');
    logged = !onLogin && (all.includes('退出登录')||all.includes('主账号')||all.includes('任务列表'));
    reason = onLogin ? 'aliyun_login_page' : (logged ? 'account_markers' : 'no_account_markers');
  } else if (platform==='vulbox') {
    const onLogin = all.includes('登录后更精彩') || all.includes('微信扫码关注即可登录');
    logged = !onLogin && (location.hostname==='user.vulbox.com' || all.includes('控制台') || all.includes('个人中心') || all.includes('我的消息'));
    reason = onLogin ? 'vulbox_login_page' : (logged ? (location.hostname==='user.vulbox.com'?'user_center':'account_markers') : 'no_account_markers');
  } else if (platform==='butian') {
    const onLogin = all.includes('白帽登录') && (all.includes('账号密码登录')||all.includes('验证码登录'));
    logged = !onLogin && (all.includes('极客空间')||all.includes('漏洞管理')||all.includes('个人中心')||all.includes('我的团队'));
    reason = onLogin ? 'butian_sso_login' : (logged ? 'account_markers' : 'no_account_markers');
  }

  return JSON.stringify({platform, logged, url:location.href, title:document.title, reason});
})()
EOF
}

check xianzhi "https://yundun.console.aliyun.com/?p=xznew#/taskmanagement/tasks/all" "xzprobe" "$STATE_DIR/bounty-xianzhi.json"
check vulbox  "https://user.vulbox.com/home" "vbprobe" "$STATE_DIR/bounty-vulbox.json"
check butian  "https://www.butian.net/WhiteHat/Center" "btprobe" "$STATE_DIR/bounty-butian.json"

python -X utf8 -I - "$TMP" <<'PY'
import json,glob,sys,os,datetime
rows=[]
for f in sorted(glob.glob(os.path.join(sys.argv[1],'*.json'))):
    raw=open(f,encoding='utf-8').read().strip()
    try:
        v=json.loads(raw)
        if isinstance(v,str): v=json.loads(v)
        rows.append(v)
    except Exception as e:
        rows.append({'platform':os.path.basename(f),'error':str(e),'raw':raw[:300]})
print(json.dumps({
    'probe':'session/check-sessions',
    'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'sessions':rows
},ensure_ascii=False,indent=2))
PY
safety_footer
