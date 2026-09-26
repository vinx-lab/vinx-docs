/* 深浅色：跟随系统，手动切换后记在本机。阅读页和后台共用同一份实现。 */
(function (global) {
  const KEY = 'docsify-x-theme';

  function read() {
    try { return localStorage.getItem(KEY); } catch (_) { return null; }
  }

  function apply(value) {
    if (value === 'dark' || value === 'light') document.documentElement.dataset.theme = value;
    else delete document.documentElement.dataset.theme;
  }

  function current() {
    const saved = read();
    if (saved) return saved;
    return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  function toggle() {
    const next = current() === 'dark' ? 'light' : 'dark';
    try { localStorage.setItem(KEY, next); } catch (_) { /* 隐私模式下只在本次会话生效 */ }
    apply(next);
    return next;
  }

  function bind(button) {
    if (button) button.addEventListener('click', toggle);
  }

  apply(read());
  global.DocsifyXTheme = { toggle, bind, current };
})(window);
