/* 外部访问（经反向代理）的登录状态：服务端在 <html data-vinx-auth> 上标明 guest（未登录）或 user，内网访问没有这个标记。
   未登录时隐藏 .needs-login 的入口，顶栏加「登录」；已登录时加「退出」。 */
(function () {
  const state = document.documentElement.dataset.vinxAuth || '';
  window.VinxAuth = { external: Boolean(state), guest: state === 'guest' };
  if (!state) return;
  document.addEventListener('DOMContentLoaded', () => {
    const theme = document.getElementById('theme-toggle');
    if (!theme) return;
    if (state === 'guest') {
      const link = document.createElement('a');
      link.className = 'ghost-button'; link.textContent = '登录';
      link.href = '/login.html?next=' + encodeURIComponent(location.pathname + location.search + location.hash);
      theme.before(link);
      return;
    }
    const button = document.createElement('button');
    button.type = 'button'; button.className = 'ghost-button'; button.textContent = '退出';
    button.addEventListener('click', async () => {
      button.disabled = true;
      try {
        await fetch('/api/logout', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      } catch (_) { /* 断网时下面刷新后仍是登录状态，可以再点 */ }
      location.reload();
    });
    theme.before(button);
  });
})();
