// Browser regression tests against the native CRUD renderer's table/card DOM.
// No production panel, machine credentials or clipboard permission is needed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.ARINODE_PLAYWRIGHT_MODULE || 'playwright');
const assets = path.join(__dirname, '../AriNode/resources/assets');
const script = fs.readFileSync(path.join(assets, 'native-table.js'), 'utf8');
const style = fs.readFileSync(path.join(assets, 'native-table.css'), 'utf8');
const command = (id, exp = Math.floor(Date.now() / 1000) + 1800) => {
    const code = Buffer.from(JSON.stringify({id, exp, key: 'fixture-only', core: 'sing', multi_node: true, multi_panel: true})).toString('base64url');
    return `curl -fsSL 'https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh' | sudo bash -s -- --enroll 'https://api.example.test' '${code}.${'a'.repeat(64)}'`;
};
const config = JSON.parse(fs.readFileSync(path.join(__dirname, '../AriNode/config.json'), 'utf8'));
const columns = config.admin_crud.servers.columns;
assert.equal(columns[4].key, 'install_command', 'Native mobile grid must include the command among its first five columns');
const sampleCommands = [1, 2, 3].map(id => command(id));
const original = sampleCommands[0];
const html = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>
*{box-sizing:border-box}body{margin:0;font:14px system-ui;background:#f8f9fb;color:#111827}main{margin:32px auto;padding:24px;background:#fff;border:1px solid #e5e7eb;border-radius:10px;max-width:1140px}h1{font-size:22px;margin:0 0 6px}p{color:#6b7280;margin:0 0 24px}.table-wrap{overflow:auto;border:1px solid #e5e7eb;border-radius:7px}table{width:100%;border-collapse:collapse}th,td{padding:14px 16px;border-bottom:1px solid #e5e7eb;text-align:left}th{font-size:12px;color:#6b7280;background:#fafbfc}td{font-size:13px}.card{display:none}.mobile-secondary>div{display:flex;justify-content:space-between;gap:8px;padding:12px}.mobile-secondary>div>div{min-width:0;flex:1;text-align:right}.mobile-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px;border:1px solid #ddd;padding:12px}.mobile-value{text-align:right;font-size:14px}
@media(max-width:767px){main{margin:16px;padding:16px}.table-wrap{display:none}.card{display:block}}
${style}</style></head><body><main><h1>AriNode 服务器</h1><p>服务器与部署命令</p><div class="table-wrap"><table><thead><tr>
${columns.map(column => `<th>${column.title}</th>`).join('')}<th>操作</th>
</tr></thead><tbody>${[1,2,3].map(id => `<tr><td>HK-0${id}</td><td>${id}</td><td>244, 258</td><td>Yes</td><td><span>${sampleCommands[id-1]}</span></td><td>2026/11/13<br>14:35:23</td><td><span>-</span></td><td>编辑</td></tr>`).join('')}</tbody></table></div>
<section class="card"><div>HK-01</div><div class="mobile-secondary"><div><span>机器 ID</span><div>1</div></div><div><span>节点 ID</span><div>244, 258</div></div></div><div class="mobile-grid">${columns.slice(3,5).map(column => `<div><div>${column.title}</div><div class="mobile-value"><span>${column.key === 'install_command' ? original : 'Yes'}</span></div></div>`).join('')}</div></section></main></body></html>`;
const settle = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));

(async () => {
    const browser = await chromium.launch({headless: true, ...(process.env.ARINODE_BROWSER_EXECUTABLE ? {executablePath: process.env.ARINODE_BROWSER_EXECUTABLE} : {})});
    try {
        const page = await browser.newPage({viewport: {width: 1280, height: 720}});
        const errors = []; page.on('pageerror', error => errors.push(error.message));
        await page.route('https://panel.example.test/**', route => route.fulfill({contentType: 'text/html', body: html}));
        await page.goto('https://panel.example.test/private-admin#/plugins/ari_node/servers');
        await page.evaluate(() => {
            window.copied = [];
            Object.defineProperty(navigator, 'clipboard', {configurable: true, value: {writeText: async value => window.copied.push(value)}});
        });
        await page.addScriptTag({content: script}); await settle(page);
        assert.equal(await page.locator('tbody .an-native-copy').count(), 3);
        assert.equal(await page.locator('.card .an-native-copy').count(), 1);
        const width = await page.locator('table').evaluate(node => node.getBoundingClientRect().width);
        assert.ok(width <= 1140, `Command expanded desktop table to ${width}px`);
        assert.ok(await page.locator('tbody .an-native-command').first().evaluate(node => node.scrollWidth > node.clientWidth));
        await page.locator('tbody .an-native-copy').first().click();
        assert.deepEqual(await page.evaluate(() => window.copied), [original]);
        assert.equal(await page.locator('tbody .an-native-copy').first().textContent(), '已复制');
        const updated = command(99);
        await page.locator('tbody .an-native-command').first().evaluate((node, value) => { node.textContent = value; }, updated);
        await settle(page); await page.locator('tbody .an-native-copy').first().click();
        assert.equal(await page.evaluate(() => window.copied.at(-1)), updated, 'Row reuse copied stale credentials');
        await page.addScriptTag({content: script}); await settle(page);
        assert.equal(await page.locator('tbody .an-native-copy').count(), 3, 'Duplicate script duplicated buttons');
        // Real React refreshes can replace or empty the span, or remove its row.
        await page.locator('tbody .an-native-command').nth(1).evaluate(node => { node.textContent = ''; });
        await settle(page); assert.equal(await page.locator('tbody .an-native-copy').count(), 2);
        await page.locator('tbody tr').nth(1).evaluate(node => node.remove());
        await settle(page);
        const expired = command(1, 1);
        await page.locator('tbody .an-native-command').first().evaluate((node, value) => { node.textContent = value; }, expired);
        await settle(page); const copied = await page.evaluate(() => window.copied.length);
        await page.locator('tbody .an-native-copy').first().click();
        assert.match(await page.locator('.an-native-notice').textContent(), /已过期/);
        assert.equal(await page.evaluate(() => window.copied.length), copied);
        await page.locator('tbody .an-native-command').first().evaluate((node, value) => { node.textContent = value; }, updated);
        await page.evaluate(() => {
            navigator.clipboard.writeText = async () => { throw new Error('permission denied'); };
            document.execCommand = () => false;
        });
        await settle(page); await page.locator('tbody .an-native-copy').first().click();
        assert.equal(await page.locator('dialog textarea').inputValue(), updated);
        assert.equal(await page.locator('dialog textarea').evaluate(node => node.selectionEnd - node.selectionStart), updated.length);
        await page.locator('dialog button').click();
        await page.locator('dialog').waitFor({state: 'detached'});
        assert.equal(await page.locator('dialog').count(), 0);
        await page.evaluate(() => { document.execCommand = () => true; });
        await page.locator('tbody .an-native-copy').first().click();
        assert.equal(await page.locator('.an-native-clipboard').count(), 0, 'Temporary credentials retained');
        await page.evaluate(() => { location.hash = '/plugins/ari_node/nodes'; }); await settle(page);
        assert.equal(await page.locator('.an-native-copy,.an-native-command').count(), 0, 'Navigation leaked table enhancement');
        await page.evaluate(() => { history.pushState({}, '', '/plugins/ari_node/servers'); });
        // pushState itself has no browser event. React commits trigger the observer.
        await page.locator('tbody tr').first().evaluate(node => node.append(document.createElement('td')));
        await page.locator('tbody tr td:last-child').first().evaluate(node => node.remove());
        await settle(page);
        assert.equal(await page.locator('tbody .an-native-copy').count(), 2);
        await page.evaluate(() => { navigator.clipboard.writeText = async value => window.copied.push(value); });
        if (process.env.ARINODE_TEST_SCREENSHOT) await page.screenshot({path: process.env.ARINODE_TEST_SCREENSHOT});
        for (const mobileWidth of [390, 320]) {
            await page.setViewportSize({width: mobileWidth, height: 844}); await settle(page);
            await page.locator('.card .an-native-copy').click();
            assert.equal(await page.evaluate(() => window.copied.at(-1)), original);
            const right = await page.locator('.card .an-native-copy').evaluate(node => node.getBoundingClientRect().right);
            assert.ok(right <= mobileWidth, 'Mobile copy button clipped');
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Mobile cards overflow viewport');
        }
        await page.locator('.card .an-native-command').evaluate(node => { node.textContent = ''; }); await settle(page);
        assert.equal(await page.locator('.card .an-native-copy').count(), 0, 'Disabled mobile server retains copy button');
        assert.deepEqual(errors, []);
        console.log('Native table browser tests passed (layout, full copy, row reuse, navigation, expiry, clipboard fallback, mobile)');
    } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
