(() => {
    'use strict';
    const key = 'xboard_plugin_theme';
    const root = document.documentElement;
    const button = document.getElementById('theme-toggle');
    const scheme = window.matchMedia('(prefers-color-scheme: dark)');
    const read = () => { try { const mode = localStorage.getItem(key); return mode === 'light' || mode === 'dark' ? mode : 'system'; } catch (_) { return 'system'; } };
    let current = read();
    const moon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20.9 13.1A9 9 0 0 1 10.9 3.1 9 9 0 1 0 20.9 13.1Z"/></svg>';
    const sun = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/></svg>';
    function icon() {
        const dark = current === 'dark' || (current === 'system' && scheme.matches);
        button.innerHTML = dark ? sun : moon; // Static icons only, never panel data.
        button.setAttribute('aria-label', dark ? '切换到浅色主题' : '切换到深色主题');
        button.title = button.getAttribute('aria-label');
    }
    function apply(mode) {
        current = mode;
        if (mode === 'system') root.removeAttribute('data-ui-theme');
        else root.setAttribute('data-ui-theme', mode);
        icon();
    }
    button.addEventListener('click', () => {
        const dark = current === 'dark' || (current === 'system' && scheme.matches);
        const mode = dark ? 'light' : 'dark';
        try { localStorage.setItem(key, mode); } catch (_) { /* Optional preference. */ }
        apply(mode);
    });
    scheme.addEventListener('change', icon);
    window.addEventListener('storage', event => { if (event.key === key) apply(read()); });
    apply(current);
})();
