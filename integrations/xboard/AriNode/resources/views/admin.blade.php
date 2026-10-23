<!doctype html>
<html lang="zh-CN">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <meta name="referrer" content="no-referrer">
    <title>AriNode · 服务器管理</title>
    <style>@include('AriNode::admin-style')</style>
</head>
<body>
<main>
    <header><div><span class="eyebrow">XBOARD PLUGIN · v0.4.0</span><h1>AriNode 服务器管理</h1><p>添加服务器、绑定节点，复制命令完成部署。</p></div><button id="refresh" class="secondary">刷新</button></header>
    <div id="notice" role="status" aria-live="polite"></div>
    <section id="auth" class="card" hidden>
        <h2>登录 XBoard 管理员</h2><p>请先在同域名的 XBoard 后台登录，再刷新本页。页面自动使用该后台的登录状态。</p>
        <details><summary>旧版后台：使用管理员登录凭证</summary><form id="auth-form"><label for="auth-token">Authorization 凭证</label><input id="auth-token" type="password" autocomplete="off" required><p class="muted">仅在当前页面内存中使用，不会写入浏览器存储。</p><button>连接</button></form></details>
    </section>
    <div id="workspace" class="layout" hidden>
        <section class="card">
            <div class="section-heading"><h2 id="form-title">添加服务器</h2><button id="new-server" class="secondary" type="button">新增</button></div>
            <form id="server-form">
                <label for="server-name">服务器名称</label><input id="server-name" maxlength="255" placeholder="例如 US-01" required>
                <label for="server-notes">备注 / IP 或域名</label><textarea id="server-notes" rows="2" maxlength="4000" placeholder="用于识别服务器"></textarea>
                <label class="check"><input id="server-active" type="checkbox" checked>启用服务器</label>
                <label for="node-search">绑定已有节点</label><input id="node-search" type="search" placeholder="搜索名称、协议或节点 ID">
                <div id="node-options" class="node-options"></div>
                <p class="muted">节点需先在 XBoard 节点管理中创建。已绑定其他服务器的节点会显示归属。</p>
                <button id="save-server" type="submit">保存并生成部署命令</button>
            </form>
        </section>
        <div class="right-column">
            <section class="card" id="deployment" hidden>
                <span class="eyebrow">DEPLOY</span><h2 id="deployment-title">一键部署</h2>
                <p>在目标服务器执行下面的命令。安装器会写入绑定配置并启动 AriNode；已有配置会备份。</p>
                <div class="command-options"><div><label for="deploy-core">内核</label><select id="deploy-core"><option value="sing">sing-box（默认）</option><option value="xray">Xray</option></select></div><div><label for="deploy-language">安装语言</label><select id="deploy-language"><option value="zh">中文</option><option value="en">English</option></select></div></div>
                <label for="deploy-command">部署命令</label><textarea id="deploy-command" class="command" rows="6" readonly spellcheck="false"></textarea>
                <div class="command-actions"><button id="copy-command" type="button">复制命令</button><span class="muted">含此服务器专属密钥，请妥善保管。</span></div>
            </section>
            <section class="card">
                <div class="section-heading"><h2>服务器</h2><span id="total" class="badge">0 台</span></div>
                <form id="search-form" class="search"><input id="server-search" type="search" placeholder="搜索服务器名称" aria-label="搜索服务器名称"><button class="secondary">搜索</button></form>
                <div id="server-list"></div>
                <div class="pagination"><button id="previous" class="secondary">上一页</button><span id="page-label"></span><button id="next" class="secondary">下一页</button></div>
            </section>
        </div>
    </div>
    <footer>AriNode · 保存绑定后，已部署机器会在下一次节点发现轮询时同步。机器上报时间不代表代理连接测试结果。</footer>
</main>
<script>@include('AriNode::admin-script')</script>
</body>
</html>
