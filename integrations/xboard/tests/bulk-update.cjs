// Browser regression coverage for the standalone server bulk updater.
// Uses the actual Blade-rendered page and mocked admin APIs; no VPS is contacted.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {chromium} = require(process.env.ARINODE_PLAYWRIGHT_MODULE || 'playwright');

const html = fs.readFileSync(process.env.ARINODE_ADMIN_HTML, 'utf8');
const machines = Array.from({length: 25}, (_, index) => ({
    id: index + 1,
    name: index === 6 ? 'Needle server' : `Server ${String(index + 1).padStart(2, '0')}`,
    is_active: index !== 24
}));
const jobs = new Map();
const requests = {queued: [], maxInFlight: 0, inFlight: 0, inventory: []};
const json = data => ({status: 200, contentType: 'application/json', body: JSON.stringify({data})});
const paginated = response => ({status: 200, contentType: 'application/json', body: JSON.stringify(response)});
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

(async () => {
    const browser = await chromium.launch({headless: true,
        ...(process.env.ARINODE_BROWSER_EXECUTABLE ? {executablePath: process.env.ARINODE_BROWSER_EXECUTABLE} : {})});
    try {
        const page = await browser.newPage({viewport: {width: 1280, height: 900}});
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        await page.addInitScript(() => localStorage.setItem('XBOARD_ACCESS_TOKEN', JSON.stringify({value: 'fixture-admin'})));
        jobs.set(22, {id: 'existing-22', state: 'pending'});
        await page.route('https://panel.example.test/**', async route => {
            const request = route.request(), url = new URL(request.url()), path = url.pathname;
            if (path === '/arinode/admin') return route.fulfill({contentType: 'text/html', body: html});
            if (path === '/api/v1/arinode/admin/nodes') return route.fulfill(paginated({data: [
                {id: 1, name: 'Node one', type: 'vless', host: 'one.example.test', enabled: true, supported: true},
                {id: 2, name: 'Node two', type: 'trojan', host: 'two.example.test', enabled: true, supported: true}
            ], total: 2}));
            if (path === '/api/v1/arinode/admin/servers') {
                const search = url.searchParams.get('search') || '';
                const filtered = machines.filter(machine => machine.name.includes(search));
                const current = Number(url.searchParams.get('current') || 1), size = Number(url.searchParams.get('pageSize') || 20);
                return route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify({
                    data: filtered.slice((current - 1) * size, current * size).map(machine => ({...machine,
                        notes: '', last_seen_at: null, node_ids: [], node_ids_text: ''})), total: filtered.length
                })});
            }
            if (path === '/api/v1/arinode/admin/backend-update/machines') {
                requests.inventory.push({...Object.fromEntries(url.searchParams), search: url.searchParams.get('search')});
                const current = Number(url.searchParams.get('current') || 1), size = Number(url.searchParams.get('pageSize') || 100);
                return route.fulfill(paginated({data: machines.slice((current - 1) * size, current * size), total: machines.length}));
            }
            if (path === '/api/v1/arinode/admin/backend-update' && request.method() === 'GET') {
                const id = Number(url.searchParams.get('id'));
                const job = jobs.get(id) || null;
                return route.fulfill(json({ready: id !== 23 && id !== 24 && id !== 25, active: id !== 25,
                    conflict: id === 23, version: '20261113-12345678', job}));
            }
            if (path === '/api/v1/arinode/admin/backend-update' && request.method() === 'POST') {
                const id = JSON.parse(request.postData()).id;
                requests.queued.push(id); requests.inFlight++;
                requests.maxInFlight = Math.max(requests.maxInFlight, requests.inFlight);
                await delay(25);
                requests.inFlight--;
                if (id === 21) return route.fulfill({status: 503, contentType: 'application/json', body: JSON.stringify({message: 'fixture queue failure'})});
                const job = {id: `job-${id}`, state: 'pending'}; jobs.set(id, job);
                return route.fulfill(json({ready: true, active: true, conflict: false, version: '20261113-12345678', job}));
            }
            return route.fulfill({status: 404, contentType: 'application/json', body: JSON.stringify({message: `Unexpected ${request.method()} ${path}`})});
        });
        await page.goto('https://panel.example.test/arinode/admin');
        await page.locator('#workspace:not([hidden])').waitFor();
        assert.equal(await page.locator('#server-list tr').count(), 20);

        await page.locator('.server-select').first().check();
        await page.locator('#next').click();
        await page.locator('#page-label').filter({hasText: '2 / 2'}).waitFor();
        await page.locator('.server-select').first().check();
        assert.equal(await page.locator('#selected-server-count').textContent(), '已选 2 台');
        await page.locator('#previous').click();
        await page.locator('.actions .button').first().click();
        await page.locator('#node-options input[type=checkbox]').first().check();
        await page.locator('#cancel-server').click();
        assert.equal(await page.locator('#selected-server-count').textContent(), '已选 2 台', 'Node binding selection altered server selection');

        await page.locator('#bulk-update-selected').click();
        await page.locator('#bulk-update-results tr').first().waitFor();
        assert.equal(await page.locator('#bulk-update-results tr').count(), 2, 'Selected preview lost cross-page selection');
        assert.deepEqual(requests.queued, [], 'Preview must not queue an update job');
        await page.locator('#start-bulk-update').click();
        await page.waitForFunction(() => document.querySelector('#bulk-update-status').textContent.includes('本次已下发'));
        assert.deepEqual(requests.queued.sort((a, b) => a - b), [1, 21]);
        assert.ok(await page.locator('#bulk-update-results tr').nth(1).textContent().then(text => text.includes('fixture queue failure')),
            'One queue failure was not shown for its machine');
        assert.ok(requests.maxInFlight <= 3);
        await page.locator('#dismiss-bulk-update').click();

        await page.locator('#server-search').fill('Needle');
        await page.locator('#search-form button').click();
        await page.locator('#bulk-update-all').click();
        await page.locator('#bulk-update-results tr').nth(24).waitFor();
        assert.equal(await page.locator('#bulk-update-results tr').count(), 25, 'Update all was constrained by search or pagination');
        await page.waitForFunction(() => document.querySelector('#bulk-update-status').textContent.includes('确认后开始下发'));
        assert.equal(requests.inventory.length, 1);
        assert.equal(requests.inventory[0].search, null, 'Inventory request unexpectedly applied a search filter');
        assert.match(await page.locator('#bulk-update-results tr').nth(21).textContent(), /已有任务/);
        assert.match(await page.locator('#bulk-update-results tr').nth(22).textContent(), /共用机器记录/);
        assert.match(await page.locator('#bulk-update-results tr').nth(23).textContent(), /离线/);
        assert.match(await page.locator('#bulk-update-results tr').nth(24).textContent(), /已停用/);
        assert.equal(requests.queued.length, 2, 'Preview queued jobs before confirmation');
        await page.locator('#start-bulk-update').click();
        await page.waitForFunction(() => document.querySelector('#bulk-update-status').textContent.includes('本次已下发'));
        assert.equal(requests.queued.length, 22, 'Eligible machines did not continue after an individual failure');
        assert.ok(requests.maxInFlight <= 3, `Batch exceeded three concurrent updates (${requests.maxInFlight})`);

        await page.setViewportSize({width: 390, height: 844});
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Mobile bulk controls overflow the viewport');
        assert.deepEqual(errors, [], `Browser errors: ${errors.join('; ')}`);
        if (process.env.ARINODE_TEST_SCREENSHOT) await page.screenshot({path: process.env.ARINODE_TEST_SCREENSHOT, fullPage: true});
        console.log('Bulk update browser tests passed (cross-page selection, explicit confirmation, search-independent all, bounded concurrency, failure isolation, skip reasons, mobile)');
    } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
