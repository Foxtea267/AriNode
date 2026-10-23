/* Visual tokens and density follow UserSpeedRiskControl's standalone UI. */
:root {
    color-scheme: light;
    --ui-bg:#fff; --ui-fg:#11161b; --ui-surface:#fff; --ui-surface-2:#f7f9fa;
    --ui-surface-3:#edf1f3; --ui-border:#dce3e8; --ui-muted:#697581;
    --ui-primary:#0b0f12; --ui-primary-fg:#fff; --ui-danger:#a42b2b; --ui-ring:#303b45;
    --ui-radius:9px; --ui-shadow:0 1px 2px rgba(16,24,32,.025);
    font-family:Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
    font-size:13px; line-height:1.5;
}
@media(prefers-color-scheme:dark) {
    :root:not([data-ui-theme="light"]) {
        color-scheme:dark; --ui-bg:#090d11; --ui-fg:#edf1f4; --ui-surface:#11171d;
        --ui-surface-2:#192129; --ui-surface-3:#262f38; --ui-border:#303a44; --ui-muted:#a2adb7;
        --ui-primary:#eef2f5; --ui-primary-fg:#0b0f12; --ui-danger:#f0a6a6; --ui-ring:#c1cbd3;
        --ui-shadow:0 2px 10px rgba(0,0,0,.12);
    }
}
:root[data-ui-theme="dark"] {
    color-scheme:dark; --ui-bg:#090d11; --ui-fg:#edf1f4; --ui-surface:#11171d;
    --ui-surface-2:#192129; --ui-surface-3:#262f38; --ui-border:#303a44; --ui-muted:#a2adb7;
    --ui-primary:#eef2f5; --ui-primary-fg:#0b0f12; --ui-danger:#f0a6a6; --ui-ring:#c1cbd3;
    --ui-shadow:0 2px 10px rgba(0,0,0,.12);
}
* { box-sizing:border-box; }
body { margin:0; min-height:100vh; color:var(--ui-fg); background:radial-gradient(circle at 12% 0%,color-mix(in srgb,var(--ui-surface-3) 24%,transparent),transparent 34%),var(--ui-bg); }
[hidden] { display:none!important; }
button,input,select,textarea { font:inherit; }
button { cursor:pointer; }
button:disabled { opacity:.45; cursor:not-allowed; }
button,input,select,textarea { transition:background-color .16s,border-color .16s,color .16s; }
button:focus-visible,input:focus-visible,select:focus-visible,textarea:focus-visible,summary:focus-visible { outline:2px solid var(--ui-ring); outline-offset:3px; }
.topbar { position:sticky; top:0; z-index:20; display:flex; align-items:center; justify-content:space-between; min-height:52px; padding-inline:max(18px,calc((100vw - 1320px)/2)); background:color-mix(in srgb,var(--ui-bg) 68%,transparent); backdrop-filter:blur(18px) saturate(1.15); }
.brand,.topbar-tools { display:flex; align-items:center; gap:10px; }
.brand { font-size:14px; font-weight:680; letter-spacing:-.02em; }
.version { font-size:10px; color:var(--ui-muted); font-weight:500; border:1px solid var(--ui-border); border-radius:4px; padding:1px 5px; letter-spacing:0; }
.topbar-tools { gap:18px; }
.theme-toggle,.icon-button { display:grid; place-items:center; width:32px; height:32px; border:0; background:transparent; color:var(--ui-fg); border-radius:6px; padding:0; }
.theme-toggle svg { width:17px; height:17px; }
.theme-toggle:hover,.icon-button:hover { background:var(--ui-surface-3); }
.icon-button { color:var(--ui-muted); font-size:24px; line-height:1; flex:none; }
.shell { width:min(1320px,calc(100% - 36px)); margin:auto; padding:26px 0 64px; }
.heading { display:flex; align-items:center; justify-content:space-between; gap:16px; margin:6px 0 22px; }
h1 { margin:0 0 6px; font-size:28px; line-height:1.2; letter-spacing:-.035em; font-weight:700; }
.heading p,.auth p { margin:0; color:var(--ui-muted); font-size:12px; }
.heading-actions { display:flex; gap:8px; flex:none; }
.button { display:inline-flex; align-items:center; justify-content:center; gap:5px; min-height:34px; padding:7px 12px; border:1px solid var(--ui-primary); border-radius:7px; background:var(--ui-primary); color:var(--ui-primary-fg); font-size:12px; font-weight:600; white-space:nowrap; }
.button:hover:not(:disabled) { filter:brightness(.88); }
.button.subtle { background:var(--ui-surface); color:var(--ui-fg); border-color:var(--ui-border); }
.button.subtle:hover:not(:disabled) { background:var(--ui-surface-2); filter:none; }
.stats { display:grid; grid-template-columns:repeat(3,1fr); gap:12px; margin-bottom:22px; }
.stat { padding:16px 18px; min-height:112px; background:var(--ui-surface); border:1px solid var(--ui-border); border-radius:var(--ui-radius); box-shadow:var(--ui-shadow); }
.stat>span,.stat small { display:block; color:var(--ui-muted); font-size:12px; }
.stat strong { display:block; margin:9px 0 6px; font-size:28px; line-height:1.15; letter-spacing:-.04em; font-weight:700; }
.stat small { font-size:11px; }
.stat strong.stat-date { font-size:17px; letter-spacing:-.02em; margin:16px 0 11px; }
.tabs { display:flex; gap:4px; padding-bottom:12px; }
.tab { display:flex; align-items:center; gap:8px; padding:8px 12px; border:1px solid transparent; border-radius:7px; background:transparent; color:var(--ui-muted); font-size:12px; font-weight:600; }
.tab:hover { background:var(--ui-surface-2); color:var(--ui-fg); }
.tab.active { background:var(--ui-surface-3); color:var(--ui-fg); }
.tab-count { font-size:10px; font-variant-numeric:tabular-nums; opacity:.65; }
.panel { background:var(--ui-surface); border:1px solid var(--ui-border); border-radius:var(--ui-radius); overflow:hidden; box-shadow:var(--ui-shadow); }
.toolbar { display:flex; align-items:center; gap:8px; padding:14px; border-bottom:1px solid var(--ui-border); }
.toolbar input[type=search] { width:min(350px,100%); flex:0 1 350px; }
.toolbar-note { color:var(--ui-muted); font-size:11px; margin-left:auto; text-align:right; }
input:not([type=checkbox]),select,textarea { min-height:36px; border:1px solid var(--ui-border); border-radius:7px; padding:8px 10px; background:var(--ui-surface); color:var(--ui-fg); width:100%; }
input::placeholder,textarea::placeholder { color:var(--ui-muted); opacity:.8; }
textarea { resize:vertical; }
.table-wrap { overflow-x:auto; }
table { width:100%; min-width:740px; border-collapse:collapse; text-align:left; }
th { padding:11px 16px; color:var(--ui-muted); background:var(--ui-surface-2); border-bottom:1px solid var(--ui-border); font-size:11px; font-weight:620; }
td { padding:16px; border-bottom:1px solid var(--ui-border); font-size:12px; vertical-align:middle; }
tbody tr:last-child td { border-bottom:0; }
tbody tr:hover td { background:var(--ui-surface-2); }
.server-name { display:block; font-weight:650; font-size:13px; margin-bottom:3px; }
.cell-note { color:var(--ui-muted); font-size:11px; max-width:290px; overflow-wrap:anywhere; }
.machine-id { font-family:ui-monospace,Consolas,monospace; }
.muted { color:var(--ui-muted); font-size:12px; }
.pill { display:inline-flex; align-items:center; gap:5px; padding:3px 7px; background:var(--ui-surface-2); color:var(--ui-muted); border:1px solid var(--ui-border); border-radius:5px; font-size:11px; white-space:nowrap; }
.pill.enabled { background:var(--ui-surface-3); color:var(--ui-fg); }
.status-dot { width:5px; height:5px; border-radius:50%; background:currentColor; opacity:.65; }
.node-pills { display:flex; flex-wrap:wrap; gap:4px; margin-bottom:4px; }
.actions { display:flex; gap:6px; justify-content:flex-end; }
.actions .button { min-height:30px; padding:5px 9px; font-size:11px; }
.actions-heading { text-align:right; }
.panel-footer { display:flex; align-items:center; justify-content:space-between; gap:12px; min-height:56px; border-top:1px solid var(--ui-border); padding:11px 14px; color:var(--ui-muted); font-size:12px; }
.pager { display:flex; align-items:center; gap:8px; white-space:nowrap; }
.pager>span { text-align:center; min-width:45px; font-variant-numeric:tabular-nums; }
.empty { padding:48px 20px; text-align:center; color:var(--ui-muted); }
.empty strong { display:block; color:var(--ui-fg); font-size:13px; font-weight:550; margin-bottom:6px; }
.page-note { margin:15px 0 0; color:var(--ui-muted); font-size:11px; }
.notice { padding:11px 13px; border:1px solid var(--ui-border); border-radius:7px; background:var(--ui-surface-2); color:var(--ui-fg); font-size:12px; margin:0 0 18px; overflow-wrap:anywhere; }
.notice.error,.form-error { color:var(--ui-danger); }
.auth { min-height:65vh; display:grid; place-items:center; }
.auth-card { width:min(420px,100%); padding:28px; border:1px solid var(--ui-border); border-radius:var(--ui-radius); background:var(--ui-surface); }
.auth h1 { font-size:25px; margin-bottom:12px; }
.auth .button { margin-top:18px; }
.auth details { border-top:1px solid var(--ui-border); margin-top:22px; padding-top:18px; }
.auth summary { color:var(--ui-muted); cursor:pointer; font-size:12px; }
.auth form label { margin-top:18px; }
dialog { padding:0; border:1px solid var(--ui-border); border-radius:12px; background:var(--ui-surface); color:var(--ui-fg); width:min(570px,calc(100% - 28px)); max-height:min(88vh,860px); box-shadow:0 18px 65px rgba(0,0,0,.18); overflow:auto; }
dialog::backdrop { background:rgba(5,10,15,.45); backdrop-filter:blur(4px); }
.deployment-dialog { width:min(730px,calc(100% - 28px)); }
.dialog-heading { display:flex; align-items:flex-start; justify-content:space-between; padding:22px 24px 18px; gap:16px; border-bottom:1px solid var(--ui-border); }
.dialog-heading h2 { margin:0 0 6px; font-size:18px; line-height:1.3; letter-spacing:-.025em; }
.dialog-heading p { margin:0; color:var(--ui-muted); font-size:12px; }
.dialog-body { padding:20px 24px; }
.field+.field,.node-field { margin-top:18px; }
label { display:block; margin-bottom:7px; font-size:12px; font-weight:550; }
.required { color:var(--ui-muted); font-weight:400; }
.field-heading { display:flex; justify-content:space-between; align-items:center; }
.field-heading .muted { font-size:11px; }
.toggle-row { display:flex; align-items:center; justify-content:space-between; margin:18px 0; border:1px solid var(--ui-border); border-radius:7px; padding:11px 13px; cursor:pointer; }
.toggle-row strong { font-size:12px; font-weight:550; }
.toggle-row small { display:block; color:var(--ui-muted); font-size:11px; margin-top:3px; font-weight:400; }
input[type=checkbox] { accent-color:var(--ui-primary); }
.toggle-row input { appearance:none; width:30px; height:17px; background:var(--ui-surface-3); border:1px solid var(--ui-border); border-radius:20px; position:relative; cursor:pointer; flex:none; }
.toggle-row input::after { content:''; display:block; width:11px; height:11px; border-radius:50%; background:var(--ui-muted); position:absolute; top:2px; left:2px; transition:transform .16s; }
.toggle-row input:checked { background:var(--ui-primary); border-color:var(--ui-primary); }
.toggle-row input:checked::after { background:var(--ui-primary-fg); transform:translateX(13px); }
.node-options { border:1px solid var(--ui-border); border-radius:7px; margin-top:9px; max-height:235px; overflow-y:auto; }
.node-option { display:flex; align-items:flex-start; gap:10px; padding:11px 12px; margin:0; border-bottom:1px solid var(--ui-border); cursor:pointer; font-weight:400; }
.node-option:last-child { border-bottom:0; }
.node-option:hover { background:var(--ui-surface-2); }
.node-option:has(input:disabled) { opacity:.5; cursor:default; }
.node-option input { margin:4px 0 0; }
.node-detail { display:block; margin-top:3px; color:var(--ui-muted); font-size:11px; }
.field-help { color:var(--ui-muted); font-size:11px; line-height:1.6; margin:8px 0 0; }
.form-error { font-size:12px; margin:14px 0 0; line-height:1.6; }
.dialog-footer { position:sticky; bottom:0; display:flex; justify-content:flex-end; gap:8px; padding:14px 24px; background:var(--ui-surface); border-top:1px solid var(--ui-border); }
.command-options { display:grid; grid-template-columns:1fr 1fr; gap:14px; margin-bottom:18px; }
.command-options .field { margin:0; }
.terminal { background:#10151b; border:1px solid #28323b; border-radius:8px; overflow:hidden; color:#d5dce2; }
.terminal-heading { display:flex; align-items:center; gap:10px; height:34px; padding:0 12px; border-bottom:1px solid #28323b; font:10px ui-monospace,Consolas,monospace; color:#8d99a5; }
.terminal-dots { display:flex; gap:4px; }
.terminal-dots i { width:5px; height:5px; border-radius:50%; background:#4c5864; }
.terminal-label { margin-left:auto; }
textarea.command { width:100%; display:block; background:transparent; color:#e1e6eb; border:0; border-radius:0; padding:16px; font:11px/1.9 ui-monospace,Consolas,monospace; min-height:165px; resize:vertical; overflow-wrap:anywhere; }
textarea.command:focus-visible { outline-offset:-3px; }
.deployment-note { margin-top:18px; padding:10px 12px; border:1px solid var(--ui-border); border-radius:7px; background:var(--ui-surface-2); color:var(--ui-muted); font-size:11px; line-height:1.7; }
.sr-only { position:absolute; width:1px; height:1px; padding:0; overflow:hidden; clip:rect(0,0,0,0); white-space:nowrap; border:0; }
@media(max-width:700px) {
    .topbar { min-height:48px; padding-inline:12px; }
    .topbar-tools { gap:10px; }
    .shell { width:calc(100% - 24px); padding-top:22px; }
    h1 { font-size:24px; }
    .heading { align-items:flex-start; flex-wrap:wrap; margin-bottom:18px; }
    .heading-actions { margin-left:auto; }
    .stats { gap:7px; margin-bottom:18px; }
    .stat { padding:12px; min-height:104px; }
    .stat>span { font-size:11px; }
    .stat strong { font-size:23px; }
    .stat strong.stat-date { font-size:12px; margin-top:14px; line-height:1.4; }
    .stat small { font-size:10px; }
    .toolbar { flex-wrap:wrap; padding:12px; }
    .toolbar input[type=search] { width:auto; flex:1; min-width:0; }
    .toolbar-note { display:none; }
    .table-wrap { overflow:visible; }
    table,tbody,tr,td { display:block; width:100%; min-width:0; }
    thead { display:none; }
    tbody tr { padding:10px 14px; border-bottom:1px solid var(--ui-border); }
    tbody tr:last-child { border-bottom:0; }
    td { display:flex; align-items:flex-start; justify-content:space-between; gap:15px; text-align:right; border:0; padding:6px 0; overflow-wrap:anywhere; }
    td::before { content:attr(data-label); color:var(--ui-muted); min-width:64px; font-size:11px; text-align:left; }
    .cell-note { max-width:240px; }
    .node-pills { justify-content:flex-end; }
    td.empty { display:block; text-align:center; padding:24px 12px; }
    td.empty::before { content:none; }
    .panel-footer { align-items:flex-start; flex-direction:column; gap:10px; }
    .pager { width:100%; justify-content:space-between; }
    .dialog-heading { padding:18px; }
    .dialog-body { padding:18px; }
    .dialog-footer { padding:13px 18px; }
    dialog { max-height:92dvh; }
}
@media(prefers-reduced-motion:reduce) {
    *,*::before,*::after { transition:none!important; scroll-behavior:auto!important; }
}
