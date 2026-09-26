/* 批注抽屉：列出一个页面（或一个短链接下全部文件）的批注，写新批注、回复、交给 agent、解决。
   阅读页和发布页共用；定位怎么画（文字高亮 / 元素圆点）由调用方通过 onChange 处理。 */
(function (global) {
  const STATUS = { open: '待处理', sent: '已交给agent', resolved: '已解决' };
  const api = (...args) => VinxEditor.api(...args);
  const el = (tag, cls, text) => { const node = document.createElement(tag); if (cls) node.className = cls; if (text !== undefined) node.textContent = text; return node; };
  const button = (text, cls) => { const b = el('button', cls || 'ghost-button', text); b.type = 'button'; return b; };
  const ago = seconds => {
    const gap = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
    if (gap < 90) return '刚刚';
    if (gap < 3600) return Math.floor(gap / 60) + ' 分钟前';
    if (gap < 86400) return Math.floor(gap / 3600) + ' 小时前';
    return Math.floor(gap / 86400) + ' 天前';
  };

  function deliveryText(item) {
    const d = item.delivery || {};
    if (item.status !== 'sent') return '';
    if (d.state === 'claimed') return d.by + ' 正在处理';
    if (d.state === 'delivered') return '已送达 ' + d.by;
    if (d.state === 'waiting') return '等待 ' + d.candidates.join('、') + ' 接收';
    return '排队中：当前没有 agent 在监听这个范围，回到 SSH 里说「处理批注」';
  }

  /* options: { query: 'target=…' 或 'prefix=…', title, describe(anchor)->文字, onChange(items), onFocus(item), badge } */
  function mount(options) {
    const drawer = el('aside', 'vinx-drawer'); drawer.hidden = true; drawer.setAttribute('aria-label', '批注');
    const head = el('div', 'vinx-drawer-head');
    const heading = el('strong', '', options.title || '批注');
    const closeButton = button('×', 'ghost-button vinx-close'); closeButton.setAttribute('aria-label', '关闭批注');
    head.append(heading, closeButton);
    const agents = el('p', 'vinx-agents');
    const compose = el('form', 'vinx-compose'); compose.hidden = true;
    const composeAnchor = el('div', 'vinx-anchor');
    const composeText = el('textarea'); composeText.rows = 4; composeText.placeholder = '写下问题或修改要求，例如：这个圆太大了，改成 32px，和标题对齐';
    const composeActions = el('div', 'vinx-actions');
    const saveOnly = button('保存批注'), saveSend = button('保存并交给 agent', 'primary-button'), cancel = button('取消');
    composeActions.append(saveSend, saveOnly, cancel);
    compose.append(el('strong', '', '新批注'), composeAnchor, composeText, composeActions);
    const tools = el('label', 'vinx-show-resolved');
    const showResolved = el('input'); showResolved.type = 'checkbox';
    tools.append(showResolved, document.createTextNode(' 显示已解决'));
    const toolsRow = el('div', 'vinx-tools');
    const whole = button(options.wholeLabel || '对整个页面写批注');
    whole.addEventListener('click', () => { const t = options.wholeTarget && options.wholeTarget(); if (t) startCompose(t.target, t.anchor, t.label); });
    toolsRow.append(tools);
    if (options.wholeTarget) toolsRow.append(whole);
    const list = el('div', 'vinx-list');
    const status = el('p', 'vinx-status'); status.setAttribute('role', 'status');
    drawer.append(head, agents, compose, toolsRow, status, list);
    document.body.append(drawer);

    let items = [], pending = null, focused = null, timer = 0, me = '';

    function setOpen(value) {
      drawer.hidden = !value;
      document.body.classList.toggle('vinx-drawer-open', value);
      if (value) refresh();
      loop();  // 打开时改成4秒一轮，关上后回到15秒
    }
    closeButton.addEventListener('click', () => { setOpen(false); cancelCompose(); });
    showResolved.addEventListener('change', render);

    function cancelCompose() { pending = null; compose.hidden = true; composeText.value = ''; options.onCompose && options.onCompose(null); }
    cancel.addEventListener('click', cancelCompose);
    async function submit(send) {
      const body = composeText.value.trim();
      if (!body) { composeText.focus(); return; }
      saveOnly.disabled = saveSend.disabled = true;
      try {
        let item = await api('/api/comments', { target: pending.target, anchor: pending.anchor, body });
        if (send) item = await api('/api/comment/update', { id: item.id, action: 'send' });
        focused = item.id; cancelCompose();
        await refresh();
        say(send ? `批注 #${item.id} 已交给 agent。${deliveryText(item)}` : `批注 #${item.id} 已保存。`);
      } catch (error) { say(error.message, 'error'); }
      finally { saveOnly.disabled = saveSend.disabled = false; }
    }
    saveOnly.addEventListener('click', () => submit(false));
    saveSend.addEventListener('click', () => submit(true));
    compose.addEventListener('submit', event => { event.preventDefault(); submit(false); });
    composeText.addEventListener('keydown', event => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') submit(true); });

    function say(text, state) { status.textContent = text || ''; status.dataset.state = state || ''; }

    async function act(item, action) {
      if (action === 'delete' && !confirm(`删除批注 #${item.id}？回复也会一起删除。`)) return;
      try {
        const result = await api('/api/comment/update', { id: item.id, action });
        await refresh();
        if (action === 'send') say(`批注 #${item.id} 已交给 agent。${deliveryText(result)}`);
      } catch (error) { say(error.message, 'error'); }
    }

    function card(item) {
      const box = el('article', 'vinx-card'); box.dataset.id = item.id; box.dataset.status = item.status;
      if (item.id === focused) box.classList.add('focused');
      const top = el('div', 'vinx-card-top');
      const number = button('#' + item.id, 'vinx-number'); number.title = '定位到页面上的位置';
      number.addEventListener('click', () => { focused = item.id; render(); options.onFocus && options.onFocus(item); });
      top.append(number, el('span', 'vinx-chip', STATUS[item.status] || item.status), el('span', 'vinx-time', ago(item.created_at)));
      box.append(top);
      const label = options.describe ? options.describe(item.anchor, item) : '';
      if (label) box.append(el('blockquote', 'vinx-quote', label));
      box.append(el('p', 'vinx-body', item.body));
      const delivery = deliveryText(item);
      if (delivery) box.append(el('p', 'vinx-delivery', delivery));
      for (const reply of item.replies) {
        const r = el('div', 'vinx-reply' + (reply.author === me ? ' mine' : ''));
        r.append(el('span', 'vinx-reply-author', reply.author + ' · ' + ago(reply.created_at)), el('p', '', reply.body));
        box.append(r);
      }
      const actions = el('div', 'vinx-actions');
      const replyButton = button('回复');
      if (item.status === 'open') { const b = button('交给 agent', 'primary-button'); b.addEventListener('click', () => act(item, 'send')); actions.append(b); }
      if (item.status === 'sent') { const b = button('撤回'); b.title = '改回待处理，agent 不再接收'; b.addEventListener('click', () => act(item, 'reopen')); actions.append(b); }
      if (item.status !== 'resolved') { const b = button('解决'); b.addEventListener('click', () => act(item, 'resolve')); actions.append(b); }
      else { const b = button('重新打开'); b.addEventListener('click', () => act(item, 'reopen')); actions.append(b); }
      const del = button('删除', 'ghost-button danger-text'); del.addEventListener('click', () => act(item, 'delete'));
      actions.append(replyButton, del);
      box.append(actions);
      replyButton.addEventListener('click', () => {
        if (box.querySelector('.vinx-reply-form')) return;
        const form = el('form', 'vinx-reply-form');
        const area = el('textarea'); area.rows = 2; area.placeholder = '补充说明或回复 agent';
        const ok = button('发送回复', 'primary-button');
        form.append(area, ok);
        const sendReply = async () => {
          if (!area.value.trim()) return;
          try { await api('/api/comment/reply', { id: item.id, body: area.value.trim() }); await refresh(); }
          catch (error) { say(error.message, 'error'); }
        };
        ok.addEventListener('click', sendReply);
        form.addEventListener('submit', event => { event.preventDefault(); sendReply(); });
        box.append(form); area.focus();
      });
      return box;
    }

    function render() {
      list.replaceChildren();
      const visible = items.filter(item => showResolved.checked || item.status !== 'resolved');
      for (const item of visible) list.append(card(item));
      if (!visible.length) list.append(el('p', 'vinx-empty', items.length ? '没有未解决的批注。' : '还没有批注。' + (options.hint || '')));
      const active = items.filter(item => item.status !== 'resolved').length;
      heading.textContent = (options.title || '批注') + (active ? ` · ${active} 条未解决` : '');
      if (options.badge) { options.badge.textContent = active ? `批注 ${active}` : '批注'; options.badge.dataset.count = active; }
      const target = list.querySelector('.vinx-card.focused');
      if (target && !drawer.hidden) target.scrollIntoView({ block: 'nearest' });
    }

    async function refresh() {
      try {
        const [data, online] = await Promise.all([api('/api/comments?' + options.query()), api('/api/agents')]);
        const changed = JSON.stringify(data.comments) !== JSON.stringify(items);
        items = data.comments;
        me = data.me || me;
        agents.textContent = online.agents.length
          ? '在线 agent：' + online.agents.map(a => a.name).join('、')
          : '当前没有 agent 在线监听；交给 agent 的批注会排队。';
        agents.dataset.online = online.agents.length ? '1' : '';
        if (changed) { render(); options.onChange && options.onChange(items); }
      } catch (error) { say(error.message, 'error'); }
    }

    function loop() {
      clearTimeout(timer);
      timer = setTimeout(async () => { if (!document.hidden) await refresh(); loop(); }, drawer.hidden ? 15000 : 4000);
    }
    loop();
    document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });

    function startCompose(target, anchor, label) {
      pending = { target, anchor };
      composeAnchor.textContent = label;
      compose.hidden = false; setOpen(true);
      composeText.focus();
    }

    return {
      open() { setOpen(true); },
      close() { setOpen(false); },
      toggle() { setOpen(drawer.hidden); },
      isOpen: () => !drawer.hidden,
      refresh,
      compose: startCompose,
      focus(id) { focused = id; setOpen(true); render(); },
      get items() { return items; },
    };
  }

  global.VinxComments = { mount, STATUS };
})(window);
