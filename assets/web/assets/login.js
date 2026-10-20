/* 登录页：成功后回到 next 指定的站内地址。 */
(function () {
  const form = document.getElementById('login-form');
  const input = document.getElementById('password');
  const status = document.getElementById('login-status');
  const button = form.querySelector('button');

  // 只接受站内路径，防止被拿来跳转到别的网站。
  function next() {
    const value = new URLSearchParams(location.search).get('next') || '/';
    return /^\/(?![\/\\])/.test(value) ? value : '/';
  }

  function show(text, state) {
    status.textContent = text; status.dataset.state = state; status.hidden = false;
  }

  fetch('/api/auth', { cache: 'no-store' }).then(r => r.ok ? r.json() : null).then(info => {
    if (info && info.authenticated && info.external) location.replace(next());
    else if (info && !info.passwordSet) show('还没有设置访问密码：请在内网打开管理后台设置。', 'warning');
  }).catch(() => {});

  form.addEventListener('submit', async event => {
    event.preventDefault();
    button.disabled = true;
    show('正在登录…', '');
    try {
      const response = await fetch('/api/login', {
        method: 'POST', cache: 'no-store', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: input.value }),
      });
      const result = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(result.error || '登录失败，请重试。');
      location.replace(next());
    } catch (error) {
      show(error.message, 'error');
      input.select();
      button.disabled = false;
    }
  });
})();
