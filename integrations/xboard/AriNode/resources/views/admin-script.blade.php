'use strict';
(() => {
    const $ = id => document.getElementById(id);
    let token = '', page = 1, total = 0, rows = [], nodes = [], editing = null, deploying = null;
    let commandRequest = 0;
    const selected = new Set();
    function notice(message, error = false) { $('notice').textContent = message; $('notice').classList.toggle('error', error); }
    function storedToken() {
        try {
            // XBoard admin-dist's official storage: uppercased prefix/key and
            // {value,time,expire}. No scans of unrelated browser credentials.
            const record = JSON.parse(localStorage.getItem('XBOARD_ACCESS_TOKEN') || 'null');
            return record && (!record.expire || record.expire > Date.now()) && typeof record.value === 'string' ? record.value : '';
        } catch (_) { return ''; }
    }
    async function api(path, body) {
        const response = await fetch('/api/v1/arinode/admin/' + path, {method: body ? 'POST' : 'GET', credentials: 'omit', cache: 'no-store', headers: {Authorization: token, Accept: 'application/json', ...(body ? {'Content-Type': 'application/json'} : {})}, ...(body ? {body: JSON.stringify(body)} : {})});
        const data = await response.json().catch(() => ({}));
        if (!response.ok) {
            if (response.status === 401 || response.status === 403) {
                $('auth').hidden = false; $('workspace').hidden = true; clearCommand();
            }
            throw new Error(Object.values(data.errors || {}).flat().join('；') || data.message || '请求失败（' + response.status + '）');
        }
        return data;
    }
    function text(tag, value, className) { const el = document.createElement(tag); el.textContent = value; if (className) el.className = className; return el; }
    function button(label, action) { const el = text('button', label); el.type = 'button'; el.className = 'secondary'; el.addEventListener('click', () => action().catch(e => notice(e.message, true))); return el; }
    function renderNodes() {
        const query = $('node-search').value.trim().toLowerCase();
        const options = $('node-options'); options.replaceChildren();
        nodes.filter(n => (n.name + ' ' + n.id + ' ' + n.type).toLowerCase().includes(query)).forEach(n => {
            const label = document.createElement('label'); label.className = 'node-option';
            const input = document.createElement('input'); input.type = 'checkbox'; input.checked = selected.has(n.id);
            input.disabled = !n.supported || (!!n.machine_id && n.machine_id !== editing);
            input.addEventListener('change', () => { if (input.checked) selected.add(n.id); else selected.delete(n.id); });
            const detail = text('span', n.name + ' · #' + n.id);
            detail.append(text('span', n.type + ' · ' + n.host + (!n.enabled ? ' · 已停用' : '') + (!n.supported ? ' · 暂不支持' : '') + (n.machine_id ? ' · 机器 #' + n.machine_id : ' · 未绑定'), 'node-detail'));
            label.append(input, detail); options.append(label);
        });
        if (!options.childElementCount) options.append(text('div', '暂无匹配节点，请先在 XBoard 创建节点。', 'empty'));
    }
    function renderServers() {
        const list = $('server-list'); list.replaceChildren();
        rows.forEach(row => {
            const card = document.createElement('article'); card.className = 'server-row';
            card.append(text('div', row.name + ' · #' + row.id, 'server-title'));
            const seen = row.last_seen_at ? new Date(row.last_seen_at * 1000).toLocaleString() : '尚未上报';
            card.append(text('div', (row.is_active ? '已启用' : '已停用') + ' · ' + row.nodes_count + ' 个节点 · 最后上报：' + seen, 'muted'));
            card.append(text('div', '节点 ID：' + (row.node_ids_text || '未绑定'), 'muted'));
            if (row.notes) card.append(text('div', row.notes, 'muted server-notes'));
            const actions = document.createElement('div'); actions.className = 'row-actions';
            actions.append(button('编辑绑定', async () => { await loadNodes(); edit(row); }), button('部署命令', async () => { deploying = row; await showCommand(); $('deployment').scrollIntoView({behavior:'smooth', block:'nearest'}); }));
            card.append(actions); list.append(card);
        });
        if (!rows.length) list.append(text('div', '还没有服务器，先添加一台。', 'empty'));
        $('total').textContent = total + ' 台'; $('page-label').textContent = '第 ' + page + ' 页';
        $('previous').disabled = page <= 1; $('next').disabled = page * 20 >= total;
    }
    function clearCommand() { commandRequest++; $('deployment').hidden = true; $('deploy-command').value = ''; $('copy-command').disabled = true; }
    function edit(row) {
        editing = row ? row.id : null; selected.clear(); (row?.node_ids || []).forEach(id => selected.add(id));
        $('server-name').value = row?.name || ''; $('server-notes').value = row?.notes || '';
        $('server-active').checked = row ? row.is_active : true; $('form-title').textContent = row ? '编辑 ' + row.name : '添加服务器';
        $('node-search').value = ''; renderNodes(); $('server-name').focus();
    }
    async function loadNodes() {
        let current = 1, all = [];
        for (;;) {
            const response = await api('nodes?current=' + current + '&pageSize=100');
            all = all.concat(response.data); if (all.length >= response.total || !response.data.length) break; current++;
        }
        nodes = all; renderNodes();
    }
    async function loadServers() {
        const response = await api('servers?current=' + page + '&pageSize=20&search=' + encodeURIComponent($('server-search').value));
        rows = response.data; total = response.total; renderServers();
    }
    async function showCommand() {
        const row = deploying; if (!row) return;
        clearCommand(); const request = commandRequest;
        // Keep selectors available after validation failures (e.g. selecting
        // Xray for AnyTLS), while never leaving the old executable command visible.
        $('deployment-title').textContent = row.name + ' · 一键部署'; $('deployment').hidden = false;
        const response = await api('command', {id: row.id, core: $('deploy-core').value, language: $('deploy-language').value});
        if (request !== commandRequest) return;
        $('deploy-command').value = response.data.command;
        $('deployment').hidden = false; $('copy-command').disabled = false;
        if ($('notice').classList.contains('error')) notice('');
    }
    async function connect() {
        if (!token) { $('auth').hidden = false; $('workspace').hidden = true; notice('请先登录同域名的 XBoard 管理员后台。'); return; }
        await loadNodes(); await loadServers(); $('auth').hidden = true; $('workspace').hidden = false; notice('');
    }
    $('server-form').addEventListener('submit', async event => {
        event.preventDefault(); $('save-server').disabled = true; clearCommand();
        try {
            const response = await api('servers/save', {...(editing ? {id: editing} : {}), name: $('server-name').value, notes: $('server-notes').value, is_active: $('server-active').checked, node_ids: [...selected]});
            editing = response.data.id; deploying = {id: editing, name: $('server-name').value};
            $('form-title').textContent = '编辑 ' + deploying.name;
            await loadNodes(); await loadServers(); notice(response.warning || '服务器已保存。');
            if ($('server-active').checked) await showCommand();
        } catch (e) { notice(e.message, true); } finally { $('save-server').disabled = false; }
    });
    $('copy-command').addEventListener('click', async () => {
        try { await navigator.clipboard.writeText($('deploy-command').value); notice('部署命令已复制。'); }
        catch (_) { $('deploy-command').focus(); $('deploy-command').select(); notice('请按 Ctrl+C / ⌘C 复制已选中的命令。'); }
    });
    $('auth-form').addEventListener('submit', event => { event.preventDefault(); token = $('auth-token').value.trim(); $('auth-token').value = ''; connect().catch(e => notice(e.message, true)); });
    $('refresh').addEventListener('click', () => { token = storedToken() || token; clearCommand(); connect().catch(e => notice(e.message, true)); });
    $('new-server').addEventListener('click', () => { clearCommand(); edit(null); });
    $('node-search').addEventListener('input', renderNodes);
    $('search-form').addEventListener('submit', event => { event.preventDefault(); page = 1; loadServers().catch(e => notice(e.message, true)); });
    for (const [id, step] of [['previous', -1], ['next', 1]]) $(id).addEventListener('click', () => { page += step; loadServers().catch(e => notice(e.message, true)); });
    for (const id of ['deploy-core', 'deploy-language']) $(id).addEventListener('change', () => showCommand().catch(e => notice(e.message, true)));
    token = storedToken(); connect().catch(e => notice(e.message, true));
})();
