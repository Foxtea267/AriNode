'use strict';
(() => {
    const $ = id => document.getElementById(id);
    let token = '', page = 1, total = 0, rows = [], nodes = [], editing = null, deploying = null;
    let commandRequest = 0, saving = false;
    let updateInfo = null, checkingUpdate = false, installingUpdate = false, updateRequest = 0;
    let backendServer = null, backendRequest = 0, backendTimer = 0, queuingBackend = false;
    let bulkUpdating = false, bulkRequest = 0, bulkTimer = 0, bulkItems = [];
    let settingsRequest = 0, savingSettings = false;
    const selected = new Set();
    const selectedServers = new Map();
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
    async function api(path, body, timeout = 0) {
        const controller = new AbortController();
        const timer = timeout ? setTimeout(() => controller.abort(), timeout) : 0;
        try {
            const response = await fetch('/api/v1/arinode/admin/' + path, {method: body ? 'POST' : 'GET', credentials: 'omit', cache: 'no-store', signal: controller.signal, headers: {Authorization: token, Accept: 'application/json', ...(body ? {'Content-Type': 'application/json'} : {})}, ...(body ? {body: JSON.stringify(body)} : {})});
            const data = await response.json().catch(() => ({}));
            if (!response.ok) {
                if (response.status === 401 || response.status === 403) {
                    $('auth').hidden = false; $('workspace').hidden = true;
                    $('server-dialog').close(); $('deployment').close(); $('plugin-update').close(); $('backend-update').close(); $('settings-dialog').close(); $('bulk-update').close(); invalidateBulk(); invalidateCommand();
                }
                throw new Error(Object.values(data.errors || {}).flat().join('；') || data.message || '请求失败（' + response.status + '）');
            }
            return data;
        } catch (e) {
            if (e.name === 'AbortError') throw new Error('请求超时，请重新读取状态；已下发的任务不会自动重试。');
            throw e;
        } finally { clearTimeout(timer); }
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
    function updateBulkControls() {
        const pageMachines = rows.filter(server => server.is_active);
        const selectedPage = pageMachines.filter(server => selectedServers.has(server.id)).length;
        const selectPage = $('select-server-page');
        selectPage.checked = pageMachines.length > 0 && selectedPage === pageMachines.length;
        selectPage.indeterminate = selectedPage > 0 && selectedPage < pageMachines.length;
        selectPage.disabled = bulkUpdating || pageMachines.length === 0;
        $('selected-server-count').textContent = '已选 ' + selectedServers.size + ' 台';
        $('bulk-update-selected').disabled = bulkUpdating || selectedServers.size === 0;
        $('clear-server-selection').disabled = bulkUpdating || selectedServers.size === 0;
        $('bulk-update-all').disabled = bulkUpdating;
        for (const input of $('server-list').querySelectorAll('.server-select')) input.disabled = bulkUpdating || input.dataset.active !== 'true';
        for (const control of $('search-form').elements) control.disabled = bulkUpdating;
        $('previous').disabled = bulkUpdating || page <= 1;
        $('next').disabled = bulkUpdating || page * 20 >= total;
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
            if (selectedServers.has(server.id)) selectedServers.set(server.id, server);
            const row = document.createElement('tr'), name = document.createElement('div');
            name.append(text('span', server.name, 'server-name'), text('div', '机器 #' + server.id + (server.notes ? ' · ' + server.notes : ''), 'cell-note'));
            cell(row, '服务器').append(name); cell(row, '状态').append(status(server.is_active));
            const bindings = document.createElement('div'), pills = document.createElement('div'); pills.className = 'node-pills';
            const protocols = [...new Set(server.node_ids.map(id => byId.get(id)?.type).filter(Boolean))];
            protocols.forEach(protocol => pills.append(text('span', protocol, 'pill')));
            bindings.append(pills, text('div', server.node_ids.length ? server.node_ids.length + ' 个节点 · ' + server.node_ids_text : '尚未绑定节点', 'cell-note'));
            cell(row, '绑定节点').append(bindings); cell(row, '最后上报').textContent = date(server.last_seen_at);
            const selectCell = cell(row, '选择'); selectCell.classList.add('selection-cell');
            const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.checked = selectedServers.has(server.id);
            checkbox.className = 'server-select'; checkbox.dataset.active = String(server.is_active);
            checkbox.disabled = bulkUpdating || !server.is_active;
            checkbox.setAttribute('aria-label', '选择 ' + server.name);
            checkbox.addEventListener('change', () => {
                if (checkbox.checked) selectedServers.set(server.id, server); else selectedServers.delete(server.id);
                updateBulkControls();
            });
            selectCell.append(checkbox); row.prepend(selectCell);
            const actions = document.createElement('div'); actions.className = 'actions';
            actions.append(button('编辑绑定', async () => openEditor(server)), button('部署命令', async () => { deploying = server; await showCommand(); }));
            if (server.is_active) actions.append(button('更新后端', async () => openBackendUpdate(server)));
            cell(row, '操作').append(actions); list.append(row);
        });
        if (!rows.length) emptyRow(list, 6, '还没有匹配的服务器', '点击右上角“添加服务器”开始部署，或调整搜索条件。');
        $('total').textContent = total; $('list-summary').textContent = '共 ' + total + ' 台服务器';
        $('page-label').textContent = page + ' / ' + Math.max(1, Math.ceil(total / 20));
        stats(); updateBulkControls();
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
    function invalidateBulk() {
        bulkRequest++; clearTimeout(bulkTimer); bulkUpdating = false;
        bulkItems = [];
        for (const id of ['close-bulk-update', 'dismiss-bulk-update']) $(id).disabled = false;
        updateBulkControls();
    }
    function bulkCurrent(request) { return request === bulkRequest && $('bulk-update').open; }
    function activeJob(data) { return ['pending', 'running'].includes(data?.job?.state); }
    function bulkMessage(item) {
        if (item.error) return item.error;
        const data = item.data;
        if (!data) return '正在读取状态…';
        const job = data.job;
        if (activeJob(data)) return (item.submitted ? '' : '跳过：已有任务 · ') + (job.state === 'pending' ? '等待节点领取' : '正在更新');
        if (item.watch && job) {
            return ({succeeded: '更新成功 · ' + (job.version || data.version || '新版本'), current: '已是最新正式版',
                failed: '更新失败，请检查 VPS 更新日志', expired: '任务超时，请核对后端版本后重试'})[job.state] || '任务状态未知';
        }
        if (data.active === false) return '跳过：服务器已停用';
        if (data.conflict) return '跳过：多台服务器共用机器记录';
        return data.ready ? '可下发更新' : '跳过：离线或未上报更新能力';
    }
    function renderBulkItem(item) {
        item.versionCell.textContent = item.data?.version || '—';
        item.resultCell.textContent = bulkMessage(item);
        item.resultCell.classList.toggle('form-error', !!item.error || ['failed', 'expired'].includes(item.data?.job?.state));
    }
    function mountBulkItems() {
        const body = $('bulk-update-results'); body.replaceChildren();
        for (const item of bulkItems) {
            const row = document.createElement('tr'); row.dataset.machineId = item.id;
            const name = cell(row, '服务器'); name.append(text('strong', item.name), text('div', '机器 #' + item.id, 'cell-note'));
            item.versionCell = cell(row, '当前版本'); item.resultCell = cell(row, '结果');
            body.append(row); renderBulkItem(item);
        }
        if (!bulkItems.length) emptyRow(body, 3, '没有可操作的服务器');
    }
    // Each machine keeps its existing authenticated queue and Redis lock. Bounded
    // workers avoid a long HTTP batch and isolate per-machine failures.
    async function bulkWork(items, request, work) {
        let index = 0, completed = 0;
        await Promise.all(Array.from({length: Math.min(3, items.length)}, async () => {
            while (bulkCurrent(request) && index < items.length) {
                const item = items[index++];
                try { await work(item); }
                catch (e) { if (bulkCurrent(request)) { item.error = e.message; item.eligible = false; } }
                if (!bulkCurrent(request)) return;
                renderBulkItem(item); $('bulk-update-progress').value = ++completed;
            }
        }));
    }
    async function bulkInventory(request) {
        const machines = new Map();
        for (let current = 1; bulkCurrent(request); current++) {
            const response = await api('backend-update/machines?current=' + current + '&pageSize=100', undefined, 20000);
            if (!bulkCurrent(request)) return [];
            for (const machine of response.data) machines.set(machine.id, machine);
            $('bulk-update-status').textContent = '正在读取全部服务器… ' + machines.size + ' / ' + response.total;
            if (current * 100 >= response.total || !response.data.length) break;
        }
        return [...machines.values()];
    }
    function bulkSummary() {
        const sent = bulkItems.filter(item => item.submitted).length;
        const active = bulkItems.filter(item => activeJob(item.data)).length;
        const done = bulkItems.filter(item => item.watch && ['succeeded', 'current'].includes(item.data?.job?.state)).length;
        const errors = bulkItems.filter(item => item.error || item.watch && ['failed', 'expired'].includes(item.data?.job?.state)).length;
        return '共 ' + bulkItems.length + ' 台 · 本次已下发 ' + sent + ' 台 · 等待/执行 ' + active + ' 台 · 成功/最新 ' + done + ' 台 · 异常 ' + errors + ' 台';
    }
    async function pollBulk(request) {
        if (!bulkCurrent(request)) return;
        const watching = bulkItems.filter(item => item.watch && (!item.data?.job || activeJob(item.data) || item.error));
        if (!watching.length) return;
        $('bulk-update-progress').max = watching.length; $('bulk-update-progress').value = 0;
        await bulkWork(watching, request, async item => {
            const response = await api('backend-update?id=' + item.id, undefined, 20000);
            if (!bulkCurrent(request)) return;
            item.data = response.data; item.error = '';
            // A timed-out POST may have queued a job. Observe it, never resend it.
            if (!item.data.job) { item.watch = false; item.error = '未发现更新任务，请重新预览后确认。'; }
        });
        if (!bulkCurrent(request)) return;
        $('bulk-update-status').textContent = bulkSummary();
        if (bulkItems.some(item => item.watch && (activeJob(item.data) || item.error))) {
            bulkTimer = setTimeout(() => pollBulk(request), 5000);
        }
    }
    async function openBulkUpdate(all) {
        if (bulkUpdating || (!all && !selectedServers.size)) return;
        invalidateBulk(); const request = bulkRequest;
        $('bulk-update-title').textContent = all ? '全部更新后端' : '批量更新后端';
        $('bulk-update-target').textContent = all ? '范围：所有分页的服务器，不受当前搜索条件限制。' : '范围：跨页选中的 ' + selectedServers.size + ' 台服务器。';
        $('bulk-update-status').textContent = '正在读取服务器状态；确认前不会下发任务。';
        $('start-bulk-update').disabled = true; $('start-bulk-update').textContent = '下发更新任务';
        $('bulk-update-progress').value = 0; $('bulk-update-results').replaceChildren();
        $('bulk-update').showModal();
        try {
            const machines = all ? await bulkInventory(request) : [...selectedServers.values()];
            if (!bulkCurrent(request)) return;
            bulkItems = machines.map(machine => ({id: machine.id, name: machine.name, eligible: false, submitted: false, watch: false, error: ''}));
            mountBulkItems(); $('bulk-update-progress').max = Math.max(1, bulkItems.length);
            await bulkWork(bulkItems, request, async item => {
                const response = await api('backend-update?id=' + item.id, undefined, 20000);
                if (!bulkCurrent(request)) return;
                item.data = response.data;
                item.eligible = item.data.ready && item.data.active !== false && !item.data.conflict && !activeJob(item.data);
                item.watch = activeJob(item.data);
            });
            if (!bulkCurrent(request)) return;
            const ready = bulkItems.filter(item => item.eligible).length;
            $('bulk-update-status').textContent = '已检查 ' + bulkItems.length + ' 台 · 可更新 ' + ready + ' 台 · 跳过/读取失败 ' + (bulkItems.length - ready) + ' 台。确认后开始下发。';
            $('start-bulk-update').disabled = ready === 0;
            if (!ready) bulkTimer = setTimeout(() => pollBulk(request), 5000);
        } catch (e) { if (bulkCurrent(request)) $('bulk-update-status').textContent = '读取失败：' + e.message; }
    }
    $('start-bulk-update').addEventListener('click', async () => {
        if (bulkUpdating || $('start-bulk-update').disabled) return;
        const request = bulkRequest, candidates = bulkItems.filter(item => item.eligible);
        clearTimeout(bulkTimer);
        bulkUpdating = true; updateBulkControls(); $('start-bulk-update').disabled = true;
        for (const id of ['close-bulk-update', 'dismiss-bulk-update']) $(id).disabled = true;
        $('bulk-update-status').textContent = '正在逐台下发任务，请等待本批下发完成。';
        $('bulk-update-progress').max = candidates.length; $('bulk-update-progress').value = 0;
        await bulkWork(candidates, request, async item => {
            item.eligible = false; item.watch = true;
            const response = await api('backend-update', {id: item.id}, 20000);
            if (!bulkCurrent(request)) return;
            item.data = response.data; item.submitted = activeJob(item.data);
            if (!item.submitted) item.error = '未创建更新任务，请重新读取状态。';
        });
        if (!bulkCurrent(request)) return;
        bulkUpdating = false; updateBulkControls();
        for (const id of ['close-bulk-update', 'dismiss-bulk-update']) $(id).disabled = false;
        $('start-bulk-update').textContent = '本批下发已结束';
        $('bulk-update-status').textContent = bulkSummary() + '。关闭后已下发任务继续执行，未完成项可重新预览。';
        bulkTimer = setTimeout(() => pollBulk(request), 5000);
    });
    for (const id of ['close-bulk-update', 'dismiss-bulk-update']) $(id).addEventListener('click', () => { if (!bulkUpdating) $('bulk-update').close(); });
    $('bulk-update').addEventListener('cancel', event => { if (bulkUpdating) event.preventDefault(); });
    $('bulk-update').addEventListener('close', invalidateBulk);
    $('bulk-update-selected').addEventListener('click', () => openBulkUpdate(false));
    $('bulk-update-all').addEventListener('click', () => openBulkUpdate(true));
    $('select-server-page').addEventListener('change', () => {
        for (const server of rows.filter(server => server.is_active)) {
            if ($('select-server-page').checked) selectedServers.set(server.id, server); else selectedServers.delete(server.id);
        }
        renderServers();
    });
    $('clear-server-selection').addEventListener('click', () => { selectedServers.clear(); renderServers(); });
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
