<!doctype html>
<html lang="zh-CN">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <meta name="robots" content="noindex,nofollow,noarchive">
    <meta name="referrer" content="no-referrer">
    <title>AriNode · 服务器管理</title>
    <script>try{const mode=localStorage.getItem('xboard_plugin_theme');if(mode==='dark'||mode==='light')document.documentElement.setAttribute('data-ui-theme',mode)}catch(_){}</script>
    <style>@include('AriNode::admin-style')</style>
</head>
<body>
<header class="topbar">
    <div class="brand">AriNode <span class="version">0.5.0</span></div>
    <div class="topbar-tools"><button id="theme-toggle" class="theme-toggle" type="button" aria-label="切换主题"></button></div>
</header>
<main class="shell">
    <section id="auth" class="auth" hidden>
        <div class="auth-card"><h1>AriNode</h1><p>请先登录同域名的 XBoard 管理员后台，再刷新本页。</p><button class="button subtle" id="auth-refresh" type="button">刷新登录状态</button>
            <details><summary>使用管理员登录凭证</summary><form id="auth-form"><label for="auth-token">Authorization 凭证</label><input id="auth-token" type="password" autocomplete="off" required><p class="muted">仅用于本次页面会话。</p><button class="button">连接</button></form></details>
        </div>
    </section>
    <div id="notice" class="notice" role="status" aria-live="polite" hidden></div>
    <section id="workspace" hidden>
        <div class="heading"><div><h1>服务器管理</h1><p>管理服务器、节点绑定与部署命令。</p></div><div class="heading-actions"><button id="open-update" class="button subtle" type="button">插件更新</button><button id="refresh" class="button subtle" type="button">刷新</button><button id="new-server" class="button" type="button"><span aria-hidden="true">＋</span> 添加服务器</button></div></div>
        <section class="stats" aria-label="服务器概览">
            <div class="stat"><span>服务器总数</span><strong id="stat-servers">—</strong><small>按当前名称搜索结果统计</small></div>
            <div class="stat"><span>可用节点</span><strong id="stat-nodes">—</strong><small>已启用且受 AriNode 支持</small></div>
            <div class="stat"><span>当前页最近上报</span><strong id="stat-report" class="stat-date">—</strong><small>机器上报时间</small></div>
        </section>
        <nav class="tabs" aria-label="管理视图" role="tablist"><button id="tab-servers" class="tab active" type="button" role="tab" aria-selected="true" aria-controls="servers-panel">服务器 <span id="total" class="tab-count">0</span></button><button id="tab-nodes" class="tab" type="button" role="tab" aria-selected="false" aria-controls="nodes-panel" tabindex="-1">节点目录 <span id="nodes-total" class="tab-count">0</span></button></nav>
        <section id="servers-panel" class="panel" role="tabpanel" aria-labelledby="tab-servers">
            <form id="search-form" class="toolbar"><input id="server-search" type="search" placeholder="搜索服务器名称" aria-label="搜索服务器名称"><button class="button subtle">搜索</button><span class="toolbar-note">选择服务器以编辑绑定或获取部署命令</span></form>
            <div class="table-wrap"><table><thead><tr><th>服务器</th><th>状态</th><th>绑定节点</th><th>最后上报</th><th class="actions-heading">操作</th></tr></thead><tbody id="server-list"></tbody></table></div>
            <div class="panel-footer"><span id="list-summary">共 0 台服务器</span><div class="pager"><button id="previous" class="button subtle" type="button">上一页</button><span id="page-label">1 / 1</span><button id="next" class="button subtle" type="button">下一页</button></div></div>
        </section>
        <section id="nodes-panel" class="panel" role="tabpanel" aria-labelledby="tab-nodes" hidden>
            <div class="toolbar"><input id="directory-search" type="search" placeholder="搜索节点名称、协议或 ID" aria-label="搜索节点目录"><span class="toolbar-note">节点协议和端口在 XBoard 节点管理中配置</span></div>
            <div class="table-wrap"><table><thead><tr><th>节点</th><th>协议</th><th>地址</th><th>所属服务器</th><th>状态</th></tr></thead><tbody id="node-directory"></tbody></table></div>
            <div class="panel-footer"><span id="directory-summary"></span></div>
        </section>
        <p class="page-note">绑定变更会由已部署的 AriNode 自动同步。机器上报时间不能替代代理连接测试。</p>
    </section>
</main>
<dialog id="server-dialog" aria-labelledby="form-title">
    <div class="dialog-heading"><div><h2 id="form-title">添加服务器</h2><p>填写服务器信息，并选择要部署的节点。</p></div><button id="close-server" class="icon-button" type="button" aria-label="关闭服务器编辑">×</button></div>
    <form id="server-form">
        <div class="dialog-body">
            <div class="field"><label for="server-name">服务器名称 <span class="required">*</span></label><input id="server-name" maxlength="255" placeholder="例如 US-01" required></div>
            <div class="field"><label for="server-notes">备注 / IP 或域名</label><textarea id="server-notes" rows="2" maxlength="4000" placeholder="用于识别这台服务器"></textarea></div>
            <label class="toggle-row" for="server-active"><span><strong>启用服务器</strong><small>启用后可生成部署命令</small></span><input id="server-active" type="checkbox" role="switch" checked></label>
            <div class="field node-field"><div class="field-heading"><label for="node-search">绑定已有节点 <span class="required">*</span></label><span id="selected-count" class="muted">已选 0 个</span></div><input id="node-search" type="search" placeholder="搜索名称、协议或节点 ID"><div id="node-options" class="node-options"></div><p class="field-help">已属于其他服务器的节点需先在原服务器中解绑。</p></div>
            <p id="server-error" class="form-error" role="alert" hidden></p>
        </div>
        <div class="dialog-footer"><button id="cancel-server" class="button subtle" type="button">取消</button><button id="save-server" class="button" type="submit">保存并生成部署命令</button></div>
    </form>
</dialog>
<dialog id="deployment" class="deployment-dialog" aria-labelledby="deployment-title">
    <div class="dialog-heading"><div><h2 id="deployment-title">一键部署</h2><p>复制命令，在目标 Linux 服务器上执行。</p></div><button id="close-deployment" class="icon-button" type="button" aria-label="关闭部署命令">×</button></div>
    <div class="dialog-body">
        <div class="command-options"><div class="field"><label for="deploy-core">内核</label><select id="deploy-core"><option value="sing">sing-box（默认）</option><option value="xray">Xray</option></select></div><div class="field"><label for="deploy-language">安装语言</label><select id="deploy-language"><option value="zh">中文</option><option value="en">English</option></select></div></div>
        <div class="terminal"><div class="terminal-heading"><span class="terminal-dots" aria-hidden="true"><i></i><i></i><i></i></span><span>bash</span><span class="terminal-label">部署命令</span></div><label class="sr-only" for="deploy-command">部署命令</label><textarea id="deploy-command" class="command" rows="7" readonly spellcheck="false"></textarea></div>
        <p id="command-status" class="field-help" role="status"></p><p id="deploy-error" class="form-error" role="alert" hidden></p>
        <div class="deployment-note">命令包含此服务器的专属密钥。已有 AriNode 配置会先备份再替换；迁移旧程序请使用对应的迁移命令。</div>
    </div>
    <div class="dialog-footer"><button id="done-deployment" class="button subtle" type="button">完成</button><button id="copy-command" class="button" type="button" disabled>复制命令</button></div>
</dialog>
<dialog id="plugin-update" aria-labelledby="update-title">
    <div class="dialog-heading"><div><h2 id="update-title">插件更新</h2><p>从 Foxtea267/AriNode 官方仓库检查并安装更新。</p></div><button id="close-update" class="icon-button" type="button" aria-label="关闭插件更新">×</button></div>
    <div class="dialog-body">
        <div class="update-versions"><div><span>当前版本</span><strong id="update-current">—</strong></div><div><span>最新版本</span><strong id="update-latest">—</strong></div></div>
        <p id="update-status" class="field-help" role="status" aria-live="polite">点击“检查更新”获取官方版本。</p>
        <p id="update-error" class="form-error" role="alert" hidden></p>
        <div class="deployment-note">更新前自动校验安装包并备份旧插件，失败时恢复旧文件和状态。服务器、节点绑定及密钥会保留；此操作仅更新 XBoard 插件。</div>
    </div>
    <div class="dialog-footer"><button id="check-update" class="button subtle" type="button">检查更新</button><button id="install-update" class="button" type="button" disabled>立即更新</button><button id="reload-update" class="button" type="button" hidden>刷新页面</button></div>
</dialog>
<script>@include('AriNode::admin-theme')</script>
<script>@include('AriNode::admin-script')</script>
</body>
</html>
