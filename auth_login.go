package main

// loginPage is the themed HTML for the admin auth login form. It is
// served on a 401 from requireAuth (or when an unauthenticated user
// navigates to /_docknap/auth/login) instead of the browser's default
// HTTP Basic Auth dialog. Placeholders {ERR_BLOCK} and {NEXT} are
// substituted by renderLogin.
const loginPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>docknap // auth</title>
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
  html, body {
    margin: 0; padding: 0;
    background: var(--bg);
    color: var(--fg);
    font-family: 'JetBrains Mono', 'Fira Code', 'Courier New', monospace;
    min-height: 100vh;
  }
  body {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 2rem 1.25rem;
  }
  .scanline {
    position: fixed; inset: 0;
    background: repeating-linear-gradient(0deg, transparent, transparent 2px, rgba(0,255,156,0.03) 2px, rgba(0,255,156,0.03) 4px);
    pointer-events: none; z-index: 100;
  }

  .auth {
    width: 100%;
    max-width: 460px;
    border: 1px solid var(--border);
    background: rgba(255,255,255,0.02);
    border-radius: 4px;
    padding: 1.75rem 1.75rem 1.4rem;
    position: relative;
    box-shadow: 0 0 0 1px rgba(0,212,255,0.04), 0 24px 60px -20px rgba(0,0,0,0.6);
  }
  .auth::before {
    content: "";
    position: absolute;
    top: -1px; left: 8%; right: 8%;
    height: 1px;
    background: linear-gradient(90deg, transparent, var(--accent), transparent);
    opacity: 0.7;
  }

  header { margin-bottom: 1.4rem; }
  .logo { font-size: 1rem; color: var(--accent); letter-spacing: 0.05em; }
  .logo::before { content: "▌ "; color: var(--fg); }
  .logo .dim { opacity: 0.45; }
  .subtitle { color: var(--dim); font-size: 0.78rem; margin-top: 0.4rem; }

  .scan {
    display: flex; align-items: center; gap: 0.75rem;
    margin-bottom: 1.4rem;
    padding: 0.55rem 0.75rem;
    background: rgba(0,212,255,0.04);
    border: 1px dashed var(--border);
    border-radius: 2px;
    font-size: 0.72rem;
    color: var(--dim);
  }
  .scan-track {
    flex: 0 0 5.5rem; height: 4px;
    background: var(--border);
    border-radius: 1px;
    position: relative; overflow: hidden;
  }
  .scan-bar {
    position: absolute; top: 0; left: -40%;
    width: 40%; height: 100%;
    background: linear-gradient(90deg, transparent, var(--accent), transparent);
    animation: scan 2.2s linear infinite;
  }
  @keyframes scan { 0% { left: -40%; } 100% { left: 100%; } }
  .scan-label b { color: var(--accent); font-weight: normal; }
  .scan-label::before { content: "// "; opacity: 0.7; }

  .err {
    margin-bottom: 1.2rem;
    padding: 0.55rem 0.75rem;
    border: 1px solid var(--err);
    color: var(--err);
    background: rgba(255,83,112,0.06);
    font-size: 0.78rem;
    border-radius: 2px;
  }
  .err::before { content: "[!] "; font-weight: bold; }

  form { display: flex; flex-direction: column; gap: 0.7rem; margin-bottom: 1.25rem; }
  .field {
    display: flex; align-items: center; gap: 0.5rem;
    border: 1px solid var(--border);
    background: rgba(0,0,0,0.25);
    padding: 0.65rem 0.8rem;
    border-radius: 2px;
    transition: border-color 0.15s ease, background 0.15s ease, box-shadow 0.15s ease;
  }
  .field:focus-within {
    border-color: var(--accent);
    background: rgba(0,212,255,0.05);
    box-shadow: 0 0 0 1px rgba(0,212,255,0.25);
  }
  .field .lbl {
    flex: 0 0 auto;
    color: var(--dim);
    font-size: 0.78rem;
    min-width: 7.5em;
    user-select: none;
    letter-spacing: 0.02em;
  }
  .field .sep { color: var(--accent); opacity: 0.6; }
  .field input {
    flex: 1; min-width: 0;
    background: transparent; border: 0; outline: 0;
    color: var(--fg);
    font: inherit; font-size: 0.9rem;
    caret-color: var(--accent);
    padding: 0;
  }
  .field input::placeholder { color: var(--dim); opacity: 0.35; }

  .actions {
    display: flex; align-items: center; justify-content: space-between;
    gap: 1rem; margin-top: 0.35rem;
  }
  .btn {
    display: inline-block;
    background: transparent;
    border: 1px solid var(--accent);
    color: var(--accent);
    padding: 0.55rem 1.2rem;
    font: inherit;
    font-size: 0.78rem;
    cursor: pointer;
    border-radius: 2px;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    transition: background 0.15s ease, color 0.15s ease;
  }
  .btn:hover { background: var(--accent); color: var(--bg); }
  .btn:active { transform: translateY(1px); }
  .btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .hint { color: var(--dim); font-size: 0.7rem; opacity: 0.7; }

  .info {
    border-top: 1px solid var(--border);
    padding-top: 0.85rem;
    font-size: 0.7rem;
    color: var(--dim);
    line-height: 1.7;
  }
  .info .line::before { content: "// "; opacity: 0.6; }
  .info b { color: var(--fg); font-weight: normal; }

  .blink { animation: blink 1.4s step-end infinite; }
  @keyframes blink { 0%, 50% { opacity: 1; } 51%, 100% { opacity: 0; } }

  footer {
    margin-top: 1.5rem;
    color: var(--dim);
    font-size: 0.7rem;
    opacity: 0.55;
    text-align: center;
  }

  @media (max-width: 480px) {
    .auth { padding: 1.4rem 1.1rem 1.1rem; }
    .field { flex-wrap: wrap; }
    .field .lbl { flex-basis: 100%; min-width: 0; }
    .scan { flex-wrap: wrap; }
  }
</style>
</head>
<body>
<div class="scanline"></div>
<div class="auth">
  <header>
    <div class="logo">DOCKNAP <span class="dim">//</span> AUTH <span class="blink">_</span></div>
    <div class="subtitle">authenticate to access the admin console</div>
  </header>
  <div class="scan">
    <div class="scan-track"><div class="scan-bar"></div></div>
    <div class="scan-label">awaiting credentials&hellip; <b>secure channel</b></div>
  </div>
  {ERR_BLOCK}
  <form method="POST" action="/_docknap/auth/login" autocomplete="on">
    <input type="hidden" name="next" value="{NEXT}">
    <div class="field">
      <span class="lbl">user@docknap</span>
      <span class="sep">:</span>
      <input id="user" type="text" name="user" required autofocus
             autocomplete="username" spellcheck="false"
             autocapitalize="off" autocorrect="off" placeholder="admin">
    </div>
    <div class="field">
      <span class="lbl">password</span>
      <span class="sep">:</span>
      <input id="pass" type="password" name="pass" required
             autocomplete="current-password" placeholder="&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;">
    </div>
    <div class="actions">
      <button type="submit" class="btn">authenticate &rarr;</button>
      <span class="hint">press enter to submit</span>
    </div>
  </form>
  <div class="info">
    <div class="line">credentials verified in-memory (<b>sha-256</b>, constant-time)</div>
    <div class="line">session cookie: <b>12h</b>, httpOnly, sameSite=Lax</div>
  </div>
</div>
<footer>// docknap.daemon &middot; powered by docknap</footer>
</body>
</html>`
