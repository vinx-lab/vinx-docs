/* 批注总览：跨文档、跨页面列出批注，可以按状态筛选、跳回原位置、交给 agent 或解决。 */
(function () {
  const list = document.getElementById('list');
  const agents = document.getElementById('agents');
  const summary = document.getElementById('summary');
  const status = document.getElementById('status');
  const STATUS = { open: '待处理', sent: '已交给agent', resolved: '已解决' };
  let filter = 'open,sent';
  let me = '';
  const el = (tag, cls, text) => { const node = document.createElement(tag); if (cls) node.className = cls; if (text !== undefined) node.textContent = text; return node; };
  const ago = seconds => {
    const gap = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
    if (gap < 90) return '刚刚';
    if (gap < 3600) return Math.floor(gap / 60) + ' 分钟前';
    if (gap < 86400) return Math.floor(gap / 3600) + ' 小时前';
    return Math.floor(gap / 86400) + ' 天前';
  };

  // 跳回原位置：文档页用 ?comment= 让阅读页打开抽屉并定位，发布页同理。
  function locate(item) {
    const where = item.where;
    if (!where) return null;
    if (where.kind === 'artifact') return '/published.html?comment=' + item.id + '#' + where.owner;
    const [path, hash] = where.url.split('#');
    return path + '?comment=' + item.id + (hash ? '#' + hash : '');
  }
  function describe(anchor) {
    if (anchor.type === 'text') return '“' + anchor.quote + '”';
    if (anchor.type === 'element') return (anchor.text ? '“' + anchor.text.slice(0, 60) + '” ' : '') + anchor.selector.split(' > ').slice(-2).join(' > ');
    if (anchor.type === 'region') return '框选区域 ' + anchor.rect.w + '×' + anchor.rect.h;
    return '整个页面';
  }
  function delivery(item) {
    const d = item.delivery || {};
    if (item.status !== 'sent') return '';
    if (d.state === 'claimed') return d.by + ' 正在处理';
    if (d.state === 'delivered') return '已送达 ' + d.by;
    if (d.state === 'waiting') return '等待 ' + d.candidates.join('、') + ' 接收';
    return '排队中：没有 agent 在监听这个范围';
  }

  async function act(item, action) {
    try { await VinxEditor.api('/api/comment/update', { id: item.id, action }); await load(); }
    catch (error) { status.textContent = error.message; status.dataset.state = 'error'; }
  }

  function render(items) {
    list.replaceChildren();
    if (!items.length) { list.textContent = '没有符合条件的批注。'; return; }
    for (const item of items) {
      const card = el('article', 'vinx-card'); card.dataset.status = item.status;
      const top = el('div', 'vinx-card-top');
      top.append(el('span', 'vinx-number', '#' + item.id), el('span', 'vinx-chip', STATUS[item.status]), el('span', 'vinx-time', ago(item.created_at)));
      const where = item.where;
      const title = el('p', 'vinx-where');
      const href = locate(item);
      if (href) { const link = el('a', '', (where.projectName ? where.projectName + ' · ' : '页面 · ') + where.title + ' · ' + where.path); link.href = href; title.append(link); }
      else title.textContent = '目标已不存在：' + item.target;
      card.append(top, title, el('blockquote', 'vinx-quote', describe(item.anchor)), el('p', 'vinx-body', item.body));
      const note = delivery(item);
      if (note) card.append(el('p', 'vinx-delivery', note));
      for (const reply of item.replies) {
        const r = el('div', 'vinx-reply' + (reply.author === me ? ' mine' : ''));
        r.append(el('span', 'vinx-reply-author', reply.author + ' · ' + ago(reply.created_at)), el('p', '', reply.body));
        card.append(r);
      }
      const actions = el('div', 'vinx-actions');
      const add = (text, action, cls) => { const b = el('button', cls || 'ghost-button', text); b.type = 'button'; b.addEventListener('click', () => act(item, action)); actions.append(b); };
      if (item.status === 'open') add('交给 agent', 'send', 'primary-button');
      if (item.status === 'sent') add('撤回', 'reopen');
      if (item.status !== 'resolved') add('解决', 'resolve'); else add('重新打开', 'reopen');
      card.append(actions);
      list.append(card);
    }
  }

  async function load() {
    try {
      const [data, online] = await Promise.all([
        VinxEditor.api('/api/comments' + (filter ? '?status=' + filter : '')),
        VinxEditor.api('/api/agents'),
      ]);
      me = data.me || me;
      render(data.comments);
      const c = data.counts;
      summary.textContent = `待处理 ${c.open} · 已交给agent ${c.sent} · 已解决 ${c.resolved}`;
      agents.textContent = online.agents.length ? '在线 agent：' + online.agents.map(a => a.name + '（' + a.scopes.join(' ') + '）').join('；') : '当前没有 agent 在线监听。';
      agents.dataset.online = online.agents.length ? '1' : '';
    } catch (error) { list.textContent = error.message; }
  }

  document.querySelectorAll('.vinx-filter button').forEach(button => button.addEventListener('click', () => {
    filter = button.dataset.status;
    document.querySelectorAll('.vinx-filter button').forEach(other => other.setAttribute('aria-selected', String(other === button)));
    load();
  }));
  DocsifyXTheme.bind(document.getElementById('theme-toggle'));
  load();
  setInterval(() => { if (!document.hidden) load(); }, 5000);
})();
