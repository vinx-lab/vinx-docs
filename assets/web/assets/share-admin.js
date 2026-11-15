/* 后台「外部发布」（spec 0008 第 4 节）：服务商设置、账号（设备码授权）、发布目标。内网或外部访问登录后可用。 */
(function () {
  const root = document.getElementById('share-admin');
  const statusLine = document.getElementById('share-status');
  if (!root) return;
  let data = null;
  let editing = null; // 正在修改的目标 id；'' 表示新增
  let connecting = null; // { account, verificationUrl, userCode, expiresAt, timer }

  const STATUS = { connected: '已连接', auth_required: '授权已失效', connecting: '连接中', failed: '连接失败' };
  const el = (tag, attrs, ...children) => {
    const node = document.createElement(tag);
    Object.entries(attrs || {}).forEach(([key, value]) => {
      if (value === undefined || value === null || value === false) return;
      if (key === 'class') node.className = value;
      else if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
      else node.setAttribute(key, value === true ? '' : value);
    });
    node.append(...children.filter(child => child !== null && child !== undefined && child !== false));
    return node;
  };
  const say = (text, state = '') => { statusLine.textContent = text; statusLine.dataset.state = state; statusLine.hidden = !text; };

  async function call(endpoint, body) {
    const options = body === undefined ? { cache: 'no-store' } : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
    const response = await fetch(endpoint, options);
    const result = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(result.error || ('操作失败：' + response.status));
    return result;
  }

  async function load() {
    try {
      data = await call('/api/share-admin');
      render();
    } catch (error) {
      root.replaceChildren(el('p', { class: 'card-note' }, error.message));
    }
  }

  // ---------------------------------------------------------------- 服务商

  function providerBlock(adapter) {
    const form = el('form', { class: 'auth-form share-provider', 'data-adapter': adapter.id });
    adapter.provider.forEach(field => {
      const input = el('input', { name: field.name, type: field.secret ? 'password' : 'text', autocomplete: 'off', spellcheck: 'false',
        value: field.secret ? '' : (field.value || ''), placeholder: field.secret && field.set ? '已设置；留空不修改' : '' });
      form.append(el('label', { class: 'field-block' }, field.description + (field.required ? '' : '（可选）'), input));
    });
    form.append(el('div', { class: 'auth-row' },
      el('button', { type: 'submit' }, '保存服务商设置'),
      adapter.docs ? el('a', { href: adapter.docs, target: '_blank', rel: 'noopener' }, '怎么创建 OAuth 客户端') : null));
    form.addEventListener('submit', async event => {
      event.preventDefault();
      const config = {}, secrets = {};
      adapter.provider.forEach(field => {
        const value = form.elements[field.name].value.trim();
        if (field.secret) secrets[field.name] = value; else config[field.name] = value;
      });
      try {
        await call('/api/share-providers/set', { adapter: adapter.id, config, secrets });
        say(adapter.name + ' 的服务商设置已保存。', 'success');
        await load();
      } catch (error) { say(error.message, 'error'); }
    });
    return el('details', { class: 'share-block', open: !adapter.ready },
      el('summary', null, '服务商设置 · ' + (adapter.ready ? '已填写' : '未填写')), form);
  }

  // ---------------------------------------------------------------- 账号

  function accountRows(adapter) {
    const accounts = data.accounts.filter(account => account.adapter === adapter.id && account.status !== 'connecting');
    const list = el('ul', { class: 'share-list' });
    accounts.forEach(account => {
      list.append(el('li', { class: 'share-item', 'data-account': account.id },
        el('div', null,
          el('strong', null, account.label),
          el('small', { class: 'share-badge', 'data-state': account.status }, STATUS[account.status] || account.status),
          el('small', null, '目标 ' + account.targets + ' 个')),
        el('div', { class: 'share-item-actions' },
          el('button', { type: 'button', class: 'ghost', onclick: () => connect(adapter, account.id) }, '重新连接'),
          el('button', { type: 'button', class: 'ghost', onclick: () => removeAccount(account) }, '断开'))));
    });
    if (!accounts.length) list.append(el('li', { class: 'share-empty' }, '还没有连接账号。'));
    return list;
  }

  async function connect(adapter, accountId) {
    stopConnecting();
    try {
      const start = await call('/api/share-accounts/connect', accountId ? { account: accountId } : { adapter: adapter.id });
      connecting = { ...start, adapter: adapter.id, expiresAt: Date.now() + start.expiresIn * 1000 };
      render();
      poll();
    } catch (error) { say(error.message, 'error'); }
  }

  function stopConnecting() {
    if (connecting && connecting.timer) clearTimeout(connecting.timer);
    connecting = null;
  }

  function poll() {
    const current = connecting;
    if (!current) return;
    current.timer = setTimeout(async () => {
      if (connecting !== current) return;
      if (Date.now() > current.expiresAt) { stopConnecting(); say('验证码已过期，请重新连接。', 'error'); render(); return; }
      try {
        const result = await call('/api/share-accounts/poll', { account: current.account });
        if (connecting !== current) return;
        if (result.status === 'done') {
          stopConnecting();
          say('已连接：' + result.account.label, 'success');
          await load();
        } else if (result.status === 'failed') {
          stopConnecting();
          say(result.error.text + (result.error.message ? '：' + result.error.message : ''), 'error');
          await load();
        } else poll();
      } catch (error) { stopConnecting(); say(error.message, 'error'); render(); }
    }, Math.max(1, current.interval || 5) * 1000);
  }

  function connectPanel() {
    if (!connecting) return null;
    const qr = el('div', { class: 'share-qr', 'aria-hidden': 'true' });
    try {
      const code = qrcode(0, 'M');
      code.addData(connecting.verificationUrl);
      code.make();
      qr.innerHTML = code.createSvgTag({ cellSize: 3, margin: 2, scalable: true });
    } catch (_) { qr.hidden = true; }
    return el('div', { class: 'share-connect', role: 'status' },
      qr,
      el('div', null,
        el('p', null, '在任意设备（电脑或手机）的浏览器里打开：'),
        el('p', null, el('a', { href: connecting.verificationUrl, target: '_blank', rel: 'noopener', class: 'share-verify-url' }, connecting.verificationUrl)),
        el('p', null, '输入验证码，登录后点「允许」：'),
        el('p', { class: 'share-code' }, connecting.userCode),
        el('p', { class: 'card-note' }, '完成后这里会自动显示已连接。'),
        el('button', { type: 'button', class: 'ghost', onclick: () => { stopConnecting(); render(); load(); } }, '取消')));
  }

  async function removeAccount(account) {
    if (!confirm('断开 ' + account.label + '？本机保存的令牌会删除，之后要重新授权才能再用。')) return;
    try {
      await call('/api/share-accounts/remove', { account: account.id });
      say('已断开 ' + account.label + '。', 'success');
      await load();
    } catch (error) { say(error.message, 'error'); }
  }

  // ---------------------------------------------------------------- 目标

  function targetRows() {
    const list = el('ul', { class: 'share-list' });
    data.targets.forEach(target => {
      const dir = target.config && target.config.dir;
      const meta = [target.adapterName || target.adapter, target.accountLabel, dir ? '目录 ' + dir : '', target.public ? '公开链接' : '仅自己可见',
        target.sources.includes('pages') ? '允许发布短链接页面' : '未开启发布'].filter(Boolean).join(' · ');
      list.append(el('li', { class: 'share-item', 'data-target': target.id },
        el('div', null,
          el('strong', null, target.label),
          el('small', null, meta + (target.live ? ' · 有效发布 ' + target.live + ' 处' : '')),
          target.problems.length ? el('small', { class: 'share-problem' }, '不可用：' + target.problems.join('；')) : null,
          target.external ? el('small', null, '外部适配器：只能在命令行修改') : null),
        el('div', { class: 'share-item-actions' },
          el('button', { type: 'button', class: 'ghost', onclick: () => testTarget(target) }, '检查'),
          target.external ? null : el('button', { type: 'button', class: 'ghost', onclick: () => { editing = target.id; render(); } }, '修改'),
          el('button', { type: 'button', class: 'ghost', onclick: () => removeTarget(target) }, '删除'))));
    });
    if (!data.targets.length) list.append(el('li', { class: 'share-empty' }, '还没有发布目标。'));
    return list;
  }

  function targetForm() {
    if (editing === null) return null;
    const target = data.targets.find(item => item.id === editing) || null;
    const adapters = data.adapters.filter(adapter => adapter.ready);
    if (!adapters.length) return el('p', { class: 'share-problem' }, '请先填写服务商设置并连接账号。');
    const form = el('form', { class: 'auth-form share-target-form' });
    const adapterSelect = el('select', { name: 'adapter' }, ...adapters.map(adapter =>
      el('option', { value: adapter.id, selected: target && target.adapter === adapter.id }, adapter.name)));
    const accountSelect = el('select', { name: 'account' });
    const configBox = el('div');
    const fill = () => {
      const adapter = adapters.find(item => item.id === adapterSelect.value);
      const accounts = data.accounts.filter(account => account.adapter === adapter.id && account.status !== 'connecting');
      accountSelect.replaceChildren(...accounts.map(account =>
        el('option', { value: account.id, selected: target && target.account === account.id }, account.label + (account.status === 'connected' ? '' : '（' + (STATUS[account.status] || account.status) + '）'))));
      configBox.replaceChildren(...adapter.config.map(field => el('label', { class: 'field-block' }, field.description,
        el('input', { name: 'config.' + field.name, type: 'text', spellcheck: 'false', value: (target && target.config[field.name]) || '' }))));
    };
    adapterSelect.addEventListener('change', fill);
    fill();
    form.append(
      el('label', { class: 'field-block' }, '显示名称（出现在「发布到」菜单里）', el('input', { name: 'label', required: true, value: target ? target.label : '' })),
      el('label', { class: 'field-block' }, '服务商', adapterSelect),
      el('label', { class: 'field-block' }, '账号', accountSelect),
      configBox,
      el('label', { class: 'field-check' }, el('input', { type: 'checkbox', name: 'pages', checked: !target || target.sources.includes('pages') }), '允许发布短链接页面'),
      el('label', { class: 'field-check' }, el('input', { type: 'checkbox', name: 'public', checked: target && target.public }), '公开链接：知道链接的任何人都能打开'),
      el('label', { class: 'field-block' }, '文件名', el('select', { name: 'naming' },
        el('option', { value: 'title', selected: !target || target.naming !== 'file' }, '按页面标题'),
        el('option', { value: 'file', selected: target && target.naming === 'file' }, '按原文件名'))),
      el('div', { class: 'auth-row' },
        el('button', { type: 'submit' }, target ? '保存修改' : '添加目标'),
        el('button', { type: 'button', class: 'ghost', onclick: () => { editing = null; render(); } }, '取消')));
    form.addEventListener('submit', async event => {
      event.preventDefault();
      const isPublic = form.elements.public.checked;
      if (isPublic && !(target && target.public) && !confirm('这个目标会生成「知道链接的任何人都能打开」的公开链接。确定？')) return;
      const config = {};
      form.querySelectorAll('[name^="config."]').forEach(input => { config[input.name.slice(7)] = input.value.trim(); });
      const body = { label: form.elements.label.value.trim(), adapter: adapterSelect.value, account: accountSelect.value, config,
        public: isPublic, sources: form.elements.pages.checked ? ['pages'] : [], naming: form.elements.naming.value };
      if (target) body.id = target.id;
      const button = form.querySelector('button[type="submit"]');
      button.disabled = true;
      say('正在检查目标…');
      try {
        const result = await call('/api/share-targets/save', body);
        editing = null;
        say('已保存：' + body.label + (result.account ? '（' + result.account + '）' : ''), 'success');
        await load();
      } catch (error) { say(error.message, 'error'); button.disabled = false; }
    });
    return form;
  }

  async function testTarget(target) {
    say('正在检查 ' + target.label + '…');
    try {
      const result = await call('/api/share-targets/test', { id: target.id });
      say('检查通过：' + target.label + '（' + result.account + '）', 'success');
    } catch (error) { say(error.message, 'error'); }
  }

  async function removeTarget(target) {
    const warning = target.live
      ? '这个目标还有 ' + target.live + ' 处有效的发布，删除后不能再更新或撤回（建议先撤回）。仍然删除？'
      : '删除目标 ' + target.label + '？已经发出去的文件不受影响。';
    if (!confirm(warning)) return;
    try {
      await call('/api/share-targets/remove', { id: target.id, force: target.live > 0 });
      say('已删除目标 ' + target.label + '。', 'success');
      await load();
    } catch (error) { say(error.message, 'error'); }
  }

  // ---------------------------------------------------------------- 整体

  function render() {
    if (!data) return;
    const blocks = data.adapters.map(adapter => el('div', { class: 'share-adapter' },
      el('h3', null, adapter.name),
      providerBlock(adapter),
      adapter.capabilities.auth ? el('div', null,
        el('h4', null, '账号'),
        accountRows(adapter),
        connecting && connecting.adapter === adapter.id ? connectPanel() : el('button', { type: 'button', disabled: !adapter.ready,
          title: adapter.ready ? '' : '先填写服务商设置', onclick: () => connect(adapter) }, '连接 ' + adapter.name + ' 账号')) : null));
    root.replaceChildren(...blocks,
      el('div', { class: 'share-adapter' },
        el('h3', null, '发布目标'),
        targetRows(),
        editing === null ? el('button', { type: 'button', onclick: () => { editing = ''; render(); } }, '新增目标') : targetForm()));
  }

  window.addEventListener('beforeunload', stopConnecting);
  // 管理页对外部访问者整体要求登录；登录后这里的接口和内网一样可用。
  load();
})();
