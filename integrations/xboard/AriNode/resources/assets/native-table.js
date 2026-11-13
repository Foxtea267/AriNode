'use strict';
(() => {
    if (window.__ariNodeNativeTable) return;
    window.__ariNodeNativeTable = true;

    const own = '.an-native-copy,.an-native-clipboard,.an-native-dialog,.an-native-notice';
    let scheduled = false, active = false, noticeTimer;
    const isServerPage = () => [location.pathname, location.hash.slice(1).split('?')[0]]
        .some(path => /(?:^|\/)plugins\/ari_node\/servers\/?$/.test(path));
    const isCommand = value => value.startsWith('curl -fsSL ')
        && value.includes('https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh')
        && value.includes(' | sudo bash');

    function element(tag, className, text) {
        const node = document.createElement(tag);
        node.className = className;
        if (text) node.textContent = text;
        return node;
    }
    function notice(message) {
        document.querySelector('.an-native-notice')?.remove();
        clearTimeout(noticeTimer);
        const node = element('div', 'an-native-notice', message);
        node.setAttribute('role', 'status');
        document.body.append(node);
        noticeTimer = setTimeout(() => node.remove(), 5000);
    }
    function expired(command) {
        const match = command.match(/ '([A-Za-z0-9_-]+)\.[a-f0-9]{64}'$/);
        if (!match) return false; // Older commands have no expiring enrollment code.
        try {
            const body = match[1].replace(/-/g, '+').replace(/_/g, '/');
            const payload = JSON.parse(atob(body.padEnd(Math.ceil(body.length / 4) * 4, '=')));
            return typeof payload.exp === 'number' && payload.exp <= Date.now() / 1000;
        } catch (_) { return false; }
    }
    function manualCopy(command, previousFocus) {
        document.querySelector('.an-native-dialog')?.remove();
        const dialog = element('dialog', 'an-native-dialog');
        const title = element('h2', '', '复制部署命令');
        title.id = 'an-native-copy-title';
        dialog.setAttribute('aria-labelledby', title.id);
        const input = element('textarea', 'an-native-clipboard');
        input.value = command; input.readOnly = true;
        input.setAttribute('aria-label', '完整部署命令');
        const close = element('button', 'an-native-copy', '完成');
        close.type = 'button';
        dialog.append(title, element('p', '', '浏览器未允许自动复制，请按 Ctrl+C / ⌘C 复制已选中的命令。'), input, close);
        const remove = () => { dialog.remove(); if (previousFocus?.isConnected) previousFocus.focus(); };
        close.addEventListener('click', () => dialog.close());
        dialog.addEventListener('close', remove);
        document.body.append(dialog);
        dialog.showModal(); input.focus(); input.select();
    }
    async function copy(command) {
        try {
            if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(command); return true; }
        } catch (_) { /* HTTP pages and denied clipboard permissions use selection. */ }
        const previousFocus = document.activeElement;
        const input = element('textarea', 'an-native-clipboard an-native-offscreen');
        input.value = command; input.readOnly = true;
        document.body.append(input); input.focus(); input.select();
        let copied = false;
        try { copied = document.execCommand('copy'); } catch (_) { /* Manual selection below. */ }
        input.remove();
        if (previousFocus?.isConnected) previousFocus.focus();
        if (!copied && isServerPage()) manualCopy(command, previousFocus);
        return copied;
    }
    function enhance(cell) {
        // Preserve React's span and its parent. Moving/wrapping React-owned nodes
        // causes reconciliation errors when rows are refreshed or removed.
        const span = [...cell.children].find(node => node.tagName === 'SPAN');
        const command = span?.textContent.trim() || '';
        const button = cell.querySelector('.an-native-copy');
        if (!isCommand(command)) {
            button?.remove(); span?.classList.remove('an-native-command');
            cell.classList.remove('an-native-command-cell');
            cell.parentElement.classList.remove('an-native-command-card');
            return;
        }
        cell.classList.add('an-native-command-cell'); span.classList.add('an-native-command');
        if (!cell.closest('table')) cell.parentElement.classList.add('an-native-command-card');
        if (button) return;
        const control = element('button', 'an-native-copy', '复制命令');
        control.type = 'button';
        control.title = '复制完整命令，注册码有效期为 30 分钟';
        control.setAttribute('aria-label', '复制完整 AriNode 部署命令');
        control.addEventListener('click', async event => {
            event.preventDefault(); event.stopPropagation();
            if (control.disabled) return;
            // Read the current row at click time: React can reuse it after sorting,
            // pagination or a refresh. Never cache credentials in data attributes.
            const current = cell.querySelector('.an-native-command')?.textContent.trim() || '';
            if (!isServerPage() || !isCommand(current)) return;
            if (expired(current)) { notice('命令已过期，请刷新列表后重新复制。'); return; }
            control.disabled = true;
            try {
                if (await copy(current)) {
                    control.textContent = '已复制';
                    setTimeout(() => { control.textContent = '复制命令'; }, 1800);
                }
            } catch (_) { notice('无法复制，请刷新列表后重试。'); }
            finally { control.disabled = false; }
        });
        cell.append(control);
    }
    function cleanup() {
        document.querySelectorAll(own).forEach(node => node.remove());
        for (const name of ['command', 'command-cell', 'command-card', 'hint']) {
            document.querySelectorAll('.an-native-' + name).forEach(node => node.classList.remove('an-native-' + name));
        }
    }
    function refresh() {
        scheduled = false;
        if (!isServerPage()) { if (active) cleanup(); active = false; return; }
        active = true;
        for (const table of document.querySelectorAll('table')) {
            const headers = [...table.querySelectorAll('thead th')];
            const index = headers.findIndex(header => header.textContent.replace(/\s/g, '').includes('AriNode一键部署命令'));
            if (index < 0) continue;
            headers[index].classList.add('an-native-command-cell');
            const hint = headers.findIndex(header => header.textContent.trim() === '部署提示');
            if (hint >= 0) headers[hint].classList.add('an-native-hint');
            for (const row of table.querySelectorAll('tbody tr')) {
                if (row.cells.length !== headers.length) continue;
                enhance(row.cells[index]);
                if (hint >= 0) row.cells[hint].classList.add('an-native-hint');
            }
        }
        // The native CRUD renderer uses cards on mobile. Find only the command
        // spans and add a sibling button in their existing cell container.
        for (const cell of document.querySelectorAll('.an-native-command-cell:not(td):not(th)')) enhance(cell);
        for (const span of document.querySelectorAll('span')) {
            if (!span.closest('table') && !span.closest(own) && isCommand(span.textContent.trim())) enhance(span.parentElement);
        }
    }
    function schedule() { if (!scheduled) { scheduled = true; requestAnimationFrame(refresh); } }
    const observer = new MutationObserver(records => {
        if (records.some(record => {
            const target = record.target.nodeType === 1 ? record.target : record.target.parentElement;
            if (target?.closest(own)) return false;
            return record.type !== 'childList' || [...record.addedNodes, ...record.removedNodes]
                .some(node => node.nodeType !== 1 || !node.matches(own));
        })) schedule();
    });
    observer.observe(document.body, {childList: true, characterData: true, subtree: true});
    window.addEventListener('hashchange', schedule);
    window.addEventListener('popstate', schedule);
    schedule();
})();
