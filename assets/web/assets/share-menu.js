/* 「发布到 ▾」：把短链接页面的源文件发布到登记的外部服务（spec 0008）。阅读页和页面列表共用。
   页面里放一个 <details class="download-menu share-menu">，用 VinxShareMenu.set(元素, 短码) 填入；
   没有可用目标或是外部访问时整个按钮隐藏。VinxShareMenu.open(元素, 短码[, 目标]) 展开菜单，或直接打开某个目标的发布框。 */
(function () {
  const STATUS = { active: '已发布', pending: '进行中', unknown: '结果未知', revoked: '已撤回' };
  const sizeText = n => n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(1) + ' MB';
  const el = (tag, attrs, ...children) => {
    const node = document.createElement(tag);
    Object.entries(attrs || {}).forEach(([key, value]) => {
      if (key === 'class') node.className = value; else if (key.startsWith('on')) node.addEventListener(key.slice(2), value); else node.setAttribute(key, value);
    });
    node.append(...children.filter(child => child !== null && child !== undefined && child !== false));
    return node;
  };
  async function api(path, body) {
    const response = await fetch(path, body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : { cache: 'no-store' });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || ('请求失败：' + response.status));
    return data;
  }

  const states = new WeakMap(); // 菜单元素 → { page, targets, shares, source }

  async function load(menu, page) {
    const [targets, shares] = await Promise.all([
      api('/api/share-targets?page=' + encodeURIComponent(page)),
      api('/api/shares?page=' + encodeURIComponent(page)),
    ]);
    const fresh = { page, targets: targets.targets || [], source: targets.source, shares: shares.shares || [], canWrite: targets.canWrite };
    // 同一个页面原地更新：打开着的发布框拿着这个对象，要看到最新的记录。
    const state = states.get(menu);
    if (state && state.page === page) return Object.assign(state, fresh);
    states.set(menu, fresh);
    return fresh;
  }

  function recordFor(state, target) { return state.shares.find(item => item.target === target); }

  function badge(record) {
    if (!record) return '';
    if (record.job) return '进行中';
    if (record.status === 'active') return record.publicMismatch ? '公开状态不符' : record.modified ? '源文件已修改' : '已发布';
    return STATUS[record.status] || '';
  }

  function render(menu) {
    const state = states.get(menu);
    const box = menu.querySelector('.download-options');
    if (!state || !state.canWrite) { menu.hidden = true; menu.open = false; return; }
    if (!state.targets.length) {
      // 内网访问、还没有可用目标：引导去后台添加。
      box.replaceChildren(el('a', { href: '/admin.html#share', target: '_top' }, el('span', null, '去后台添加发布目标'),
        el('small', null, '连接网盘账号后，这里会列出可以发布到的位置')));
      menu.hidden = false;
      return;
    }
    box.replaceChildren(...state.targets.map(target => {
      const record = recordFor(state, target.id);
      const status = target.accountStatus === 'auth_required' ? '账号授权已失效' : badge(record);
      const link = el('a', { href: '#', 'data-target': target.id },
        el('span', null, target.label),
        el('small', null, [target.account, target.public ? '公开链接' : '仅自己可见', status].filter(Boolean).join(' · ')));
      link.addEventListener('click', event => { event.preventDefault(); menu.open = false; dialog(menu, target.id); });
      return link;
    }));
    menu.hidden = false;
  }

  async function set(menu, page) {
    if (!menu) return;
    bind(menu);
    menu.dataset.page = page || '';
    if (!page) { menu.hidden = true; menu.open = false; return; }
    try {
      await load(menu, page);
      if (menu.dataset.page === page) render(menu);
    } catch (error) { menu.hidden = true; }
  }

  function bind(menu) {
    if (menu.dataset.bound) return;
    menu.dataset.bound = '1';
    document.addEventListener('click', event => { if (menu.open && !menu.contains(event.target)) menu.open = false; });
    menu.addEventListener('keydown', event => { if (event.key === 'Escape' && menu.open) { menu.open = false; menu.querySelector('summary').focus(); } });
    // 打开菜单时刷新状态（源文件是否改过）。
    menu.addEventListener('toggle', () => { if (menu.open && menu.dataset.page) load(menu, menu.dataset.page).then(() => render(menu)).catch(() => {}); });
  }

  // ---------------------------------------------------------------- 发布框

  let current = null; // 打开着的发布框

  function dialog(menu, targetId) {
    const state = states.get(menu);
    const target = state && state.targets.find(item => item.id === targetId);
    if (!target) return;
    const initial = target;
    if (current) current.close();
    const box = el('dialog', { class: 'share-dialog', 'aria-labelledby': 'share-dialog-title' });
    const body = el('div', { class: 'share-dialog-body' });
    box.append(
      el('div', { class: 'share-dialog-head' },
        el('h2', { id: 'share-dialog-title' }, '发布到 ' + target.label),
        el('button', { type: 'button', class: 'ghost-button', 'aria-label': '关闭', onclick: () => box.close() }, '×')),
      body);
    box.addEventListener('close', () => { box.remove(); if (current === box) current = null; refresh(); });
    document.body.append(box);
    current = box;
    box.showModal();

    let timer = null;
    const refresh = () => load(menu, state.page).then(() => render(menu)).catch(() => {});
    const row = (label, value) => el('div', { class: 'share-row' }, el('span', null, label), el('strong', null, value));

    function draw(message) {
      // 刷新后 state.targets 是新的对象：按 id 取最新的（例如账号刚被标为授权失效）。
      const target = state.targets.find(item => item.id === targetId) || initial;
      const record = recordFor(state, target.id);
      const source = state.source || { files: 0, size: 0, rejected: [] };
      const caps = target.capabilities || {};
      const rows = [
        row('目标', target.label + '（' + target.adapter + '）'),
        target.account ? row('账号', target.account) : null,
        row('链接', target.public ? '公开：知道链接的任何人都能打开' : '仅自己可见'),
        row('文件名', target.name),
        row('内容', source.files + ' 个文件' + (source.bundled ? '，打包成 zip' : '') + '，共 ' + sizeText(source.size)),
      ];
      if (record) rows.push(row('状态', badge(record) || STATUS[record.status]));
      const parts = [el('div', { class: 'share-rows' }, ...rows.filter(Boolean))];
      const reauth = target.accountStatus === 'auth_required';
      if (reauth) parts.push(el('p', { class: 'share-error' }, '账号 ' + target.account + ' 的授权已失效，',
        el('a', { href: '/admin.html#share', target: '_top' }, '去后台重新连接'), '后再发布或撤回。'));
      if (source.rejected && source.rejected.length) {
        parts.push(el('p', { class: 'share-error' }, '有文件没通过发布检查，不能发布：' + source.rejected.join('、')));
      }
      if (record && record.url && record.status !== 'revoked') parts.push(linkBox(record.url));
      if (record && record.lastError) {
        const err = record.lastError;
        parts.push(el('p', { class: 'share-error' }, err.text + (err.message ? '：' + err.message : '') + hint(err.code)));
      }
      if (message) parts.push(message);
      const actions = el('div', { class: 'share-actions' });
      const button = (text, fn, primary) => el('button', { type: 'button', class: primary ? 'primary-button' : 'ghost-button', onclick: fn }, text);
      const blocked = (source.rejected && source.rejected.length) || reauth;
      if (!record || record.status === 'revoked') {
        if (!blocked) actions.append(button(record ? '重新发布' : '发布', () => run('publish'), true));
      } else if (record.status === 'active') {
        if (caps.update && !blocked) actions.append(button('更新', () => run('publish'), true));
        if (caps.revoke && !reauth) actions.append(button('撤回', () => run('revoke')));
      } else if (record.status === 'unknown') {
        if (!blocked) actions.append(button('重试', () => run('publish'), true));
        if (caps.revoke && record.remoteId && !reauth) actions.append(button('撤回', () => run('revoke')));
      }
      body.replaceChildren(...parts, actions);
    }

    function linkBox(url) {
      const copy = el('button', { type: 'button', class: 'ghost-button' }, '复制');
      copy.addEventListener('click', () => {
        navigator.clipboard.writeText(url).then(() => { copy.textContent = '已复制'; }, () => { copy.textContent = '复制失败，请手动选择'; });
      });
      return el('div', { class: 'share-link' }, el('a', { href: url, target: '_blank', rel: 'noopener' }, url), copy);
    }

    function hint(code) {
      if (code === 'auth_required') return '（请到后台「外部发布」重新连接这个账号）';
      if (code === 'public_mismatch') return '（请立即撤回）';
      if (code === 'timeout' || code === 'crashed') return '（可以重试；重试会覆盖原来的位置，不会产生重复文件）';
      return '';
    }

    async function run(kind) {
      const record = recordFor(state, target.id);
      if (kind === 'revoke' && !confirm('撤回会删除服务商那边的文件，链接随之失效。确定撤回？')) return;
      body.querySelectorAll('.share-actions button').forEach(b => { b.disabled = true; });
      try {
        const data = kind === 'publish'
          ? await api('/api/shares', { page: state.page, target: target.id })
          : await api('/api/shares/revoke', { share: record.id });
        poll(data.job.id, Date.now());
      } catch (error) {
        draw(el('p', { class: 'share-error' }, error.message));
      }
    }

    function poll(id, started) {
      const progress = el('p', { class: 'share-progress', role: 'status' }, '发送中…');
      body.querySelectorAll('.share-actions button').forEach(b => { b.disabled = true; });
      body.append(progress);
      const tick = async () => {
        if (!box.open) return;
        progress.textContent = '发送中… ' + Math.floor((Date.now() - started) / 1000) + ' 秒';
        let job;
        try { job = await api('/api/shares/job?id=' + encodeURIComponent(id)); } catch (error) { progress.textContent = error.message; return; }
        if (job.status === 'running') { timer = setTimeout(tick, 800); return; }
        await load(menu, state.page).catch(() => {});
        render(menu);
        const done = job.status === 'done';
        draw(el('p', { class: done ? 'share-ok' : 'share-error', role: 'status' },
          done ? (job.kind === 'revoke' ? '已撤回。' : '已发布。') : job.status === 'unknown' ? '结果未知。' : '没有成功。'));
      };
      timer = setTimeout(tick, 300);
    }

    box.addEventListener('close', () => clearTimeout(timer));
    draw();
    // 有进行中的任务时接着显示进度。
    const record = recordFor(state, target.id);
    if (record && record.job) poll(record.job, Date.now());
  }

  async function open(menu, page, targetId) {
    await set(menu, page);
    const state = states.get(menu);
    if (!state || !state.targets.length) return false;
    // 没指定目标时展开菜单让用户自己选，不默认选第一个（可能是公开链接的目标）。
    if (targetId) dialog(menu, targetId);
    else { menu.open = true; menu.querySelector('summary').focus(); }
    return true;
  }

  window.VinxShareMenu = { set, open };
})();
