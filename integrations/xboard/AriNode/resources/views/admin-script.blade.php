'use strict';
(() => {
    const $ = id => document.getElementById(id);
    let token = '', page = 1, total = 0, rows = [], nodes = [], editing = null, deploying = null;
    let commandRequest = 0, saving = false;
    let updateInfo = null, checkingUpdate = false, installingUpdate = false, updateRequest = 0;
    let backendServer = null, backendRequest = 0, backendTimer = 0, queuingBackend = false;
    let settingsRequest = 0, savingSettings = false;
    const selected = new Set();
    function notice(message, error = false) {
        $('notice').textContent = message; $('notice').hidden = !message;
        $('notice').classList.toggle('error', error);
    }
    function errorAt(id, message = '') { $(id).textContent = message; $(id).hidden = !message; }
    function storedToken() {
        try {
            const record = JSON.parse(localStorage.getItem('XBOARD_ACCESS_TOKEN') || 'null');
            return record && (!record.expire || record.expire > Date.now()) && typeof record.value === 'string' ? record.value : '';
        } catch (_) { return ''; }
    }
    async function api(path, body) {
        const response = await fetch('/api/v1/arinode/admin/' + path, {method: body ? 'POST' : 'GET', credentials: 'omit', cache: 'no-store', headers: {Authorization: token, Accept: 'application/json', ...(body ? {'Content-Type': 'application/json'} : {})}, ...(body ? {body: JSON.stringify(body)} : {})});
        const data = await response.json().catch(() => ({}));
        if (!response.ok) {
            if (response.status === 401 || response.status === 403) {
                $('auth').hidden = false; $('workspace').hidden = true;
                $('server-dialog').close(); $('deployment').close(); $('plugin-update').close(); $('backend-update').close(); $('settings-dialog').close(); invalidateCommand();
            }
            throw new Error(Object.values(data.errors || {}).flat().join('；') || data.message || '请求失败（' + response.status + '）');
        }
        return data;
    }
    function text(tag, value, className) {
        const el = document.createElement(tag); el.textContent = value;
        if (className) el.className = className;
        return el;
    }
    function cell(row, label) {
        const el = document.createElement('td'); el.dataset.label = label; row.append(el); return el;
    }
    function button(label, action) {
        const el = text('button', label, 'button subtle'); el.type = 'button';
        el.addEventListener('click', () => action().catch(e => notice(e.message, true)));
        return el;
    }
    function emptyRow(target, columns, title, subtitle = '') {
        const row = document.createElement('tr'), td = document.createElement('td'); td.colSpan = columns; td.className = 'empty';
        td.append(text('strong', title), text('span', subtitle)); row.append(td); target.append(row);
    }
    function status(enabled, supported = true) {
        const pill = text('span', '', 'pill' + (enabled && supported ? ' enabled' : ''));
        pill.append(text('span', '', 'status-dot'), text('span', !supported ? '暂不支持' : enabled ? '已启用' : '已停用'));
        return pill;
    }
    function date(value) { return value ? new Date(value * 1000).toLocaleString() : '尚未上报'; }
    function stats() {
        $('stat-servers').textContent = total;
        $('stat-nodes').textContent = nodes.filter(n => n.enabled && n.supported).length;
        const latest = Math.max(0, ...rows.map(row => Number(row.last_seen_at) || 0));
        $('stat-report').textContent = latest ? date(latest) : '尚未上报';
    }
    function renderNodes() {
        const query = $('node-search').value.trim().toLowerCase(), options = $('node-options'); options.replaceChildren();
        nodes.filter(n => (n.name + ' ' + n.id + ' ' + n.type).toLowerCase().includes(query)).forEach(n => {
            const label = document.createElement('label'); label.className = 'node-option';
            const input = document.createElement('input'); input.type = 'checkbox'; input.checked = selected.has(n.id);
            input.disabled = (!n.supported && !selected.has(n.id)) || (!!n.machine_id && n.machine_id !== editing);
            input.addEventListener('change', () => {
                if (input.checked) selected.add(n.id); else selected.delete(n.id);
                $('selected-count').textContent = '已选 ' + selected.size + ' 个';
                if (!n.supported) renderNodes();
            });
            const detail = text('span', n.name + ' · #' + n.id);
            detail.append(text('span', n.type + ' · ' + n.host + (!n.enabled ? ' · 已停用' : '') + (!n.supported ? ' · 暂不支持' : '') + (n.machine_id ? ' · 机器 #' + n.machine_id : ' · 未绑定'), 'node-detail'));
            label.append(input, detail); options.append(label);
        });
        $('selected-count').textContent = '已选 ' + selected.size + ' 个';
        if (!options.childElementCount) options.append(text('div', '没有匹配的节点', 'empty'));
    }
    function renderDirectory() {
        const query = $('directory-search').value.trim().toLowerCase();
        const filtered = nodes.filter(n => (n.name + ' ' + n.id + ' ' + n.type + ' ' + n.host).toLowerCase().includes(query));
        const body = $('node-directory'); body.replaceChildren();
        filtered.forEach(n => {
            const row = document.createElement('tr'), title = text('div', n.name, 'server-name');
            const name = document.createElement('div'); name.append(title, text('div', '#' + n.id, 'cell-note machine-id'));
            cell(row, '节点').append(name); cell(row, '协议').append(text('span', n.type, 'pill'));
            cell(row, '地址').append(text('span', n.host, 'cell-note'));
            cell(row, '所属服务器').textContent = n.machine_id ? '机器 #' + n.machine_id : '未绑定';
            cell(row, '状态').append(status(n.enabled, n.supported)); body.append(row);
        });
        if (!filtered.length) emptyRow(body, 5, '没有匹配的节点', '请在 XBoard 节点管理中创建节点，或调整搜索条件。');
        $('directory-summary').textContent = '显示 ' + filtered.length + ' / ' + nodes.length + ' 个节点';
        $('nodes-total').textContent = nodes.length;
    }
    function renderServers() {
        const list = $('server-list'); list.replaceChildren(); const byId = new Map(nodes.map(n => [n.id, n]));
        rows.forEach(server => {
            const row = document.createElement('tr'), name = document.createElement('div');
            name.append(text('span', server.name, 'server-name'), text('div', '机器 #' + server.id + (server.notes ? ' · ' + server.notes : ''), 'cell-note'));
            cell(row, '服务器').append(name); cell(row, '状态').append(status(server.is_active));
            const bindings = document.createElement('div'), pills = document.createElement('div'); pills.className = 'node-pills';
            const protocols = [...new Set(server.node_ids.map(id => byId.get(id)?.type).filter(Boolean))];
            protocols.forEach(protocol => pills.append(text('span', protocol, 'pill')));
            bindings.append(pills, text('div', server.node_ids.length ? server.node_ids.length + ' 个节点 · ' + server.node_ids_text : '尚未绑定节点', 'cell-note'));
            cell(row, '绑定节点').append(bindings); cell(row, '最后上报').textContent = date(server.last_seen_at);
            const actions = document.createElement('div'); actions.className = 'actions';
            actions.append(button('编辑绑定', async () => openEditor(server)), button('部署命令', async () => { deploying = server; await showCommand(); }));
            if (server.is_active) actions.append(button('更新后端', async () => openBackendUpdate(server)));
            cell(row, '操作').append(actions); list.append(row);
        });
        if (!rows.length) emptyRow(list, 5, '还没有匹配的服务器', '点击右上角“添加服务器”开始部署，或调整搜索条件。');
        $('total').textContent = total; $('list-summary').textContent = '共 ' + total + ' 台服务器';
        $('page-label').textContent = page + ' / ' + Math.max(1, Math.ceil(total / 20));
        $('previous').disabled = page <= 1; $('next').disabled = page * 20 >= total; stats();
    }
    function invalidateCommand() {
        commandRequest++; $('deploy-command').value = ''; $('copy-command').disabled = true;
        $('deploy-api-url').textContent = '';
        $('command-status').textContent = ''; errorAt('deploy-error');
    }
    async function openEditor(server) {
        editing = server ? server.id : null; selected.clear(); (server?.node_ids || []).forEach(id => selected.add(id));
        $('server-name').value = server?.name || ''; $('server-notes').value = server?.notes || '';
        $('server-active').checked = server ? server.is_active : true;
        $('form-title').textContent = server ? '编辑服务器' : '添加服务器';
        $('node-search').value = ''; errorAt('server-error'); renderNodes();
        if (!$('server-dialog').open) $('server-dialog').showModal();
        $('server-name').focus();
        try { await loadNodes(); } catch (e) { errorAt('server-error', e.message); }
    }
    async function loadNodes() {
        let current = 1, all = [];
        for (;;) {
            const response = await api('nodes?current=' + current + '&pageSize=100');
            all = all.concat(response.data); if (all.length >= response.total || !response.data.length) break; current++;
        }
        nodes = all; renderNodes(); renderDirectory(); stats();
    }
    async function loadServers() {
        const response = await api('servers?current=' + page + '&pageSize=20&search=' + encodeURIComponent($('server-search').value));
        rows = response.data; total = response.total; renderServers();
    }
    async function showCommand() {
        const server = deploying; if (!server) return;
        invalidateCommand(); const request = commandRequest;
        $('deployment-title').textContent = server.name + ' · 一键部署'; $('command-status').textContent = '正在生成部署命令…';
        if (!$('deployment').open) $('deployment').showModal();
        try {
            const response = await api('command', {id: server.id, core: $('deploy-core').value, multi_node: $('deploy-multi-node').checked, multi_panel: $('deploy-multi-panel').checked});
            if (request !== commandRequest || !$('deployment').open) return;
            $('deploy-command').value = response.data.command; $('copy-command').disabled = false;
            $('deploy-api-url').textContent = 'API 通讯地址 ' + response.data.api_url;
            $('command-status').textContent = '命令已就绪';
        } catch (e) {
            if (request !== commandRequest) return;
            $('command-status').textContent = ''; errorAt('deploy-error', e.message);
        }
    }
    async function connect() {
        if (!token) { $('auth').hidden = false; $('workspace').hidden = true; return; }
        await loadNodes(); await loadServers(); $('auth').hidden = true; $('workspace').hidden = false; notice('');
    }
    function showSettings(data) {
        $('settings-api-url').value = data.api_url;
        $('settings-effective').textContent = data.effective_url ? '当前生效地址 ' + data.effective_url : '';
        errorAt('settings-error', data.warning || '');
    }
    $('open-settings').addEventListener('click', async () => {
        const request = ++settingsRequest;
        $('settings-api-url').value = ''; $('settings-api-url').disabled = true;
        $('save-settings').disabled = true; errorAt('settings-error');
        $('settings-effective').textContent = '正在读取通讯地址…';
        $('settings-dialog').showModal();
        try {
            const response = await api('settings');
            if (request !== settingsRequest || !$('settings-dialog').open) return;
            showSettings(response.data);
            $('settings-api-url').disabled = false; $('save-settings').disabled = false;
            $('settings-api-url').focus();
        } catch (e) {
            if (request === settingsRequest) {
                $('settings-effective').textContent = ''; errorAt('settings-error', e.message);
            }
        }
    });
    $('settings-form').addEventListener('submit', async event => {
        event.preventDefault();
        if (savingSettings || $('save-settings').disabled) return;
        savingSettings = true; errorAt('settings-error');
        const request = ++settingsRequest;
        for (const id of ['save-settings', 'close-settings', 'cancel-settings', 'settings-api-url']) $(id).disabled = true;
        try {
            const response = await api('settings', {api_url: $('settings-api-url').value.trim()});
            if (request !== settingsRequest || !$('settings-dialog').open) return;
            showSettings(response.data); invalidateCommand();
            $('settings-dialog').close(); notice('API 通讯地址已保存。');
            await loadServers();
        } catch (e) {
            if ($('settings-dialog').open) errorAt('settings-error', e.message);
            else notice(e.message, true);
        } finally {
            savingSettings = false;
            for (const id of ['save-settings', 'close-settings', 'cancel-settings', 'settings-api-url']) $(id).disabled = false;
        }
    });
    for (const id of ['close-settings', 'cancel-settings']) $(id).addEventListener('click', () => { if (!savingSettings) $('settings-dialog').close(); });
    $('settings-dialog').addEventListener('cancel', event => { if (savingSettings) event.preventDefault(); });
    $('settings-dialog').addEventListener('close', () => { settingsRequest++; });
    function tab(name, focus = false) {
        for (const item of ['servers', 'nodes']) {
            const active = item === name, control = $('tab-' + item);
            control.classList.toggle('active', active); control.setAttribute('aria-selected', String(active)); control.tabIndex = active ? 0 : -1;
            $(item + '-panel').hidden = !active;
        }
        if (focus) $('tab-' + name).focus();
    }
    function busy(value) {
        saving = value;
        for (const id of ['save-server', 'close-server', 'cancel-server']) $(id).disabled = value;
        $('save-server').textContent = value ? '正在保存…' : '保存并生成部署命令';
    }
    function showBackendUpdate(data) {
        $('backend-update-current').textContent = data.version || '—';
        const job = data.job, state = job?.state;
        const active = state === 'pending' || state === 'running';
        $('send-backend-update').disabled = !data.ready || active || queuingBackend;
        const messages = {
            pending: '更新任务已下发，等待节点领取。',
            running: '节点正在更新，请等待重启后的检查结果。',
            succeeded: '更新成功，后端已运行 ' + (job?.version || '新版本') + '。',
            current: '后端已经是最新正式版。',
            failed: '更新失败，请在 VPS 查看 journalctl -u arinode-panel-upgrade.service。',
            expired: '任务超时，请核对后端版本后重试。'
        };
        $('backend-update-status').textContent = messages[state] || (data.ready ? '后端已就绪。' : data.conflict
            ? '同一机器记录收到多台服务器的上报，请给各 VPS 使用独立机器记录。'
            : '节点尚未上报更新能力。旧版请先在 VPS 执行 sudo anctl upgrade。');
    }
    async function refreshBackendUpdate(queue = false) {
        const server = backendServer, request = ++backendRequest;
        if (!server) return;
        clearTimeout(backendTimer);
        errorAt('backend-update-error');
        if (queue) {
            queuingBackend = true;
            $('send-backend-update').disabled = true;
        }
        try {
            const response = await (queue ? api('backend-update', {id: server.id}) : api('backend-update?id=' + server.id));
            if (request !== backendRequest || !$('backend-update').open) return;
            queuingBackend = false;
            showBackendUpdate(response.data);
        } catch (e) {
            if (request === backendRequest) errorAt('backend-update-error', e.message);
        } finally {
            if (request === backendRequest && $('backend-update').open) {
                queuingBackend = false;
                backendTimer = setTimeout(() => refreshBackendUpdate(), 5000);
            }
        }
    }
    function openBackendUpdate(server) {
        backendServer = server;
        $('backend-update-target').textContent = server.name;
        $('backend-update-current').textContent = '—';
        $('backend-update-status').textContent = '正在读取后端状态…';
        $('send-backend-update').disabled = true;
        $('backend-update').showModal();
        return refreshBackendUpdate();
    }
    $('close-backend-update').addEventListener('click', () => $('backend-update').close());
    $('backend-update').addEventListener('close', () => {
        backendRequest++;
        clearTimeout(backendTimer);
        backendServer = null;
        queuingBackend = false;
    });
    $('send-backend-update').addEventListener('click', () => {
        if (!queuingBackend && !$('send-backend-update').disabled) refreshBackendUpdate(true);
    });
    async function checkUpdate() {
        if (checkingUpdate || installingUpdate) return;
        checkingUpdate = true; updateInfo = null; const request = ++updateRequest;
        $('check-update').disabled = true; $('install-update').disabled = true;
        $('update-latest').textContent = '—'; errorAt('update-error');
        $('update-status').textContent = '正在检查官方版本…';
        try {
            const response = await api('update');
            if (request !== updateRequest || !$('plugin-update').open) return;
            updateInfo = response.data;
            $('update-current').textContent = updateInfo.current_version;
            $('update-latest').textContent = updateInfo.latest_version;
            $('install-update').disabled = !updateInfo.available;
            $('update-status').textContent = updateInfo.available ? '发现新版本，可以立即更新。来源：' + updateInfo.source : '当前已是最新版本。';
        } catch (e) { if (request === updateRequest) { $('update-status').textContent = ''; errorAt('update-error', e.message); } }
        finally { checkingUpdate = false; $('check-update').disabled = false; }
    }
    $('open-update').addEventListener('click', () => {
        $('update-current').textContent = document.querySelector('.version').textContent;
        $('plugin-update').showModal(); if ($('reload-update').hidden) checkUpdate();
    });
    $('check-update').addEventListener('click', checkUpdate);
    $('close-update').addEventListener('click', () => { if (!installingUpdate) $('plugin-update').close(); });
    $('plugin-update').addEventListener('cancel', event => { if (installingUpdate) event.preventDefault(); });
    $('plugin-update').addEventListener('close', () => { updateRequest++; updateInfo = null; });
    $('reload-update').addEventListener('click', () => location.reload());
    $('install-update').addEventListener('click', async () => {
        if (!updateInfo?.available || installingUpdate || saving) return;
        installingUpdate = true; $('install-update').disabled = true;
        $('close-update').disabled = true; $('check-update').disabled = true; errorAt('update-error');
        $('update-status').textContent = '正在下载、校验、备份并更新插件，请稍候…';
        try {
            const response = await api('update', {version: updateInfo.latest_version, commit: updateInfo.commit});
            $('update-current').textContent = response.data.version;
            $('update-status').textContent = '更新成功。旧版备份编号：' + response.data.backup + '。刷新页面加载新版。';
            if (response.data.warning) errorAt('update-error', response.data.warning);
            $('install-update').hidden = true; $('check-update').hidden = true; $('reload-update').hidden = false;
        } catch (e) {
            $('update-status').textContent = '请重新检查版本，确认当前状态后重试。'; errorAt('update-error', e.message);
        } finally { installingUpdate = false; updateInfo = null; $('close-update').disabled = false; $('check-update').disabled = false; }
    });
    $('server-form').addEventListener('submit', async event => {
        event.preventDefault(); if (saving) return;
        busy(true); errorAt('server-error');
        try {
            const name = $('server-name').value, active = $('server-active').checked;
            const response = await api('servers/save', {...(editing ? {id: editing} : {}), name, notes: $('server-notes').value, is_active: active, node_ids: [...selected]});
            editing = response.data.id; deploying = {id: editing, name}; $('server-dialog').close();
            await loadNodes(); await loadServers(); notice(response.warning || '服务器已保存。');
            if (active) await showCommand();
        } catch (e) { if ($('server-dialog').open) errorAt('server-error', e.message); else notice(e.message, true); }
        finally { busy(false); }
    });
    $('copy-command').addEventListener('click', async () => {
        if ($('copy-command').disabled) return;
        try { await navigator.clipboard.writeText($('deploy-command').value); $('command-status').textContent = '部署命令已复制'; }
        catch (_) { $('deploy-command').focus(); $('deploy-command').select(); $('command-status').textContent = '请按 Ctrl+C / ⌘C 复制已选中的命令。'; }
    });
    $('deployment').addEventListener('close', invalidateCommand);
    for (const id of ['close-deployment', 'done-deployment']) $(id).addEventListener('click', () => $('deployment').close());
    for (const id of ['close-server', 'cancel-server']) $(id).addEventListener('click', () => { if (!saving) $('server-dialog').close(); });
    $('server-dialog').addEventListener('cancel', event => { if (saving) event.preventDefault(); });
    $('auth-form').addEventListener('submit', event => { event.preventDefault(); token = $('auth-token').value.trim(); $('auth-token').value = ''; connect().catch(e => notice(e.message, true)); });
    const refresh = () => { token = storedToken() || token; invalidateCommand(); connect().catch(e => notice(e.message, true)); };
    $('refresh').addEventListener('click', refresh); $('auth-refresh').addEventListener('click', refresh);
    $('new-server').addEventListener('click', () => openEditor(null));
    $('node-search').addEventListener('input', renderNodes); $('directory-search').addEventListener('input', renderDirectory);
    $('search-form').addEventListener('submit', event => { event.preventDefault(); page = 1; loadServers().catch(e => notice(e.message, true)); });
    for (const [id, step] of [['previous', -1], ['next', 1]]) $(id).addEventListener('click', () => { page += step; loadServers().catch(e => notice(e.message, true)); });
    for (const id of ['deploy-core', 'deploy-multi-node', 'deploy-multi-panel']) $(id).addEventListener('change', showCommand);
    for (const name of ['servers', 'nodes']) {
        $('tab-' + name).addEventListener('click', () => tab(name));
        $('tab-' + name).addEventListener('keydown', event => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
            event.preventDefault(); tab(event.key === 'Home' ? 'servers' : event.key === 'End' ? 'nodes' : name === 'servers' ? 'nodes' : 'servers', true);
        });
    }
    token = storedToken(); connect().catch(e => notice(e.message, true));
})();
