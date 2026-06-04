package main

import "net/http"

const adminPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>docknap // admin</title>
<meta name="robots" content="noindex,nofollow">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  :root {
    --bg: #0a0e14;
    --fg: #00ff9c;
    --dim: #4a6a5a;
    --accent: #00d4ff;
    --border: #1a2a22;
    --warn: #ffb454;
    --err: #ff5370;
    --btn: #2a4a3a;
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; background: var(--bg); color: var(--fg); font-family: 'JetBrains Mono', 'Fira Code', 'Courier New', monospace; min-height: 100vh; }
  body { padding: 2rem; max-width: 1100px; margin: 0 auto; }
  .scanline { position: fixed; inset: 0; background: repeating-linear-gradient(0deg, transparent, transparent 2px, rgba(0,255,156,0.03) 2px, rgba(0,255,156,0.03) 4px); pointer-events: none; z-index: 100; }
  header { border-bottom: 1px solid var(--border); padding-bottom: 1rem; margin-bottom: 1.5rem; display: flex; justify-content: space-between; align-items: flex-end; }
  .logo { font-size: 1rem; color: var(--accent); }
  .logo::before { content: "▌ "; color: var(--fg); }
  .stats { display: flex; gap: 1.5rem; font-size: 0.78rem; color: var(--dim); }
  .stats b { color: var(--fg); font-weight: normal; }
  table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
  th { text-align: left; color: var(--dim); font-weight: normal; text-transform: uppercase; font-size: 0.7rem; letter-spacing: 0.1em; padding: 0.5rem 0.75rem; border-bottom: 1px solid var(--border); }
  td { padding: 0.85rem 0.75rem; border-bottom: 1px solid var(--border); vertical-align: middle; }
  tr:hover td { background: rgba(0,255,156,0.02); }
  .sub { color: var(--accent); }
  .sub b { color: var(--accent); }
  .cont { color: var(--dim); font-size: 0.78rem; }
  .pill { display: inline-block; padding: 0.15rem 0.5rem; border: 1px solid currentColor; border-radius: 2px; font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .pill.running { color: var(--fg); }
  .pill.exited, .pill.dead, .pill.missing { color: var(--err); }
  .pill.created, .pill.restarting, .pill.paused { color: var(--warn); }
  .pill.unknown { color: var(--dim); }
  .uptime { color: var(--dim); font-size: 0.78rem; }
  .btn { display: inline-block; background: transparent; border: 1px solid var(--btn); color: var(--fg); padding: 0.3rem 0.7rem; font: inherit; font-size: 0.72rem; cursor: pointer; border-radius: 2px; text-transform: uppercase; letter-spacing: 0.05em; margin-right: 0.4rem; }
  .btn:hover { border-color: var(--accent); color: var(--accent); }
  .btn.stop:hover { border-color: var(--err); color: var(--err); }
  .btn:disabled { opacity: 0.4; cursor: not-allowed; }
  .btn:disabled:hover { border-color: var(--btn); color: var(--fg); }
  .actions { white-space: nowrap; }
  .logout-form { display: inline-block; margin: 0; padding: 0; }
  .logout-btn {
    display: inline-block;
    background: transparent;
    border: 1px solid var(--border);
    color: var(--dim);
    padding: 0.3rem 0.7rem;
    font: inherit;
    font-size: 0.72rem;
    cursor: pointer;
    border-radius: 2px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    transition: border-color 0.15s, color 0.15s;
  }
  .logout-btn:hover { border-color: var(--err); color: var(--err); }
  .logout-btn:focus-visible { outline: 2px solid var(--err); outline-offset: 2px; }
  .empty { text-align: center; color: var(--dim); padding: 3rem 1rem; }
  .empty .icon { font-size: 2rem; color: var(--dim); margin-bottom: 0.5rem; }
  .toast { position: fixed; bottom: 1.5rem; right: 1.5rem; background: var(--bg); border: 1px solid var(--border); padding: 0.6rem 1rem; font-size: 0.78rem; opacity: 0; transition: opacity 0.3s; pointer-events: none; z-index: 200; }
  .toast.show { opacity: 1; }
  .toast.ok { border-color: var(--fg); color: var(--fg); }
  .toast.err { border-color: var(--err); color: var(--err); }
  footer { margin-top: 2rem; color: var(--dim); font-size: 0.7rem; text-align: center; opacity: 0.6; }
  .blink { animation: blink 1.4s step-end infinite; }
  @keyframes blink { 0%, 50% { opacity: 1; } 51%, 100% { opacity: 0; } }
</style>
</head>
<body>
<div class="scanline"></div>
<header>
  <div>
    <div class="logo">DOCKNAP // ADMIN <span class="blink">_</span></div>
  </div>
  <div class="stats">
    <div>registered <b id="stat-reg">0</b></div>
    <div>running <b id="stat-run">0</b></div>
    <div>refresh <b id="stat-refresh">2s</b></div>
    <form class="logout-form" method="POST" action="/_docknap/auth/logout">
      <button class="logout-btn" type="submit" title="clear session cookie">logout</button>
    </form>
  </div>
</header>
<table>
  <thead>
    <tr>
      <th>subdomain</th>
      <th>container</th>
      <th>port</th>
      <th>state</th>
      <th>uptime</th>
      <th>idle</th>
      <th>actions</th>
    </tr>
  </thead>
  <tbody id="rows"></tbody>
</table>
<div class="toast" id="toast"></div>
<footer>// docknap.daemon &middot; auto-refresh every 2s</footer>
<script>
const rows = document.getElementById('rows');
const statReg = document.getElementById('stat-reg');
const statRun = document.getElementById('stat-run');
const toast = document.getElementById('toast');

let toastTimer = null;
function showToast(msg, kind) {
  toast.textContent = msg;
  toast.className = 'toast show ' + (kind || 'ok');
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.className = 'toast', 2200);
}

function fmtUptime(s) {
  if (s == null) return '—';
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s/60) + 'm ' + (s%60) + 's';
  if (s < 86400) return Math.floor(s/3600) + 'h ' + Math.floor((s%3600)/60) + 'm';
  return Math.floor(s/86400) + 'd ' + Math.floor((s%86400)/3600) + 'h';
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]);
}

function render(services) {
  if (!services || services.length === 0) {
    rows.innerHTML = '<tr><td colspan="7"><div class="empty"><div class="icon">∅</div>no registered services</div></td></tr>';
    statReg.textContent = '0';
    statRun.textContent = '0';
    return;
  }
  statReg.textContent = services.length;
  statRun.textContent = services.filter(s => s.state === 'running').length;
  rows.innerHTML = services.map(s => {
    const state = s.state || 'unknown';
    const actions = state === 'running'
      ? '<button class="btn stop" data-act="stop" data-sub="' + escapeHTML(s.subdomain) + '">stop</button>'
      : '<button class="btn" data-act="wake" data-sub="' + escapeHTML(s.subdomain) + '">wake</button>';
    return '<tr>' +
      '<td class="sub"><b>' + escapeHTML(s.subdomain) + '</b></td>' +
      '<td class="cont">' + escapeHTML(s.container) + '</td>' +
      '<td>' + s.target_port + '</td>' +
      '<td><span class="pill ' + escapeHTML(state) + '">' + escapeHTML(state) + '</span></td>' +
      '<td class="uptime">' + fmtUptime(s.uptime_s) + '</td>' +
      '<td class="uptime">' + escapeHTML(s.idle_timeout) + '</td>' +
      '<td class="actions">' + actions + '</td>' +
    '</tr>';
  }).join('');
}

async function refresh() {
  try {
    const res = await fetch('/_docknap/status', { cache: 'no-store' });
    const data = await res.json();
    render(data.services);
  } catch (e) {
    showToast('status fetch failed: ' + e.message, 'err');
  }
}

document.addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const sub = btn.dataset.sub;
  const act = btn.dataset.act;
  btn.disabled = true;
  try {
    const method = act === 'stop' ? 'POST' : 'GET';
    const res = await fetch('/_docknap/' + act + '/' + encodeURIComponent(sub), { method });
    if (!res.ok) {
      const text = await res.text();
      showToast(act + ' ' + sub + ' failed: ' + text, 'err');
    } else {
      showToast(act + ': ' + sub, 'ok');
      setTimeout(refresh, 300);
    }
  } catch (err) {
    showToast(act + ' ' + sub + ' failed: ' + err.message, 'err');
  }
  setTimeout(() => { btn.disabled = false; }, 1000);
});

refresh();
setInterval(refresh, 5000);
</script>
</body>
</html>`

func (s *Docknap) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/_docknap" && r.URL.Path != "/_docknap/" && r.URL.Path != "/_docknap/ui" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(adminPage))
}
