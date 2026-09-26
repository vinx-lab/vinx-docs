/* 页面：左侧按发布时间倒序列出，右侧直接显示。默认跟随最新发布，手动选了旧的就停在那一个。
   顶栏可以点选批注（页面里的 artifact-live.js 负责点选和画编号）、查看批注、编辑源码。 */
(function () {
  const list = document.getElementById('artifact-list');
  const frame = document.getElementById('artifact-frame');
  const empty = document.getElementById('artifact-empty');
  const openNew = document.getElementById('open-new');
  const currentTitle = document.getElementById('current-title');
  const followNote = document.getElementById('follow-note');
  const pickButton = document.getElementById('pick-comment');
  const commentToggle = document.getElementById('comment-toggle');
  const editSelect = document.getElementById('edit-file');
  const editButton = document.getElementById('edit-source');
  const EDITABLE = /\.(html?|css|m?js|json|svg|md|markdown|txt|xml|ya?ml|csv)$/i;
  const params = new URLSearchParams(location.search);
  let items = [];
  let selected = decodeURIComponent(location.hash.slice(1));
  let follow = !selected;
  let shownKey = '';
  let framePage = '';
  let pendingFocus = Number(params.get('comment')) || 0;

  const relative = seconds => {
    const gap = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
    if (gap < 90) return '刚刚';
    if (gap < 3600) return Math.floor(gap / 60) + ' 分钟前';
    if (gap < 86400) return Math.floor(gap / 3600) + ' 小时前';
    return Math.floor(gap / 86400) + ' 天前';
  };
  const prefix = () => 'a:' + selected + '/';
  const toFrame = message => frame.contentWindow && frame.contentWindow.postMessage(Object.assign({ vinx: true }, message), '*');

  function describe(anchor, item) {
    const page = item ? item.target.slice(item.target.indexOf('/') + 1) : anchor.page || '';
    const where = page ? page + ' · ' : '';
    if (anchor.type === 'region') return where + '框选区域 ' + anchor.rect.w + '×' + anchor.rect.h;
    if (anchor.type === 'element') return where + (anchor.text ? '“' + anchor.text.slice(0, 60) + '”' : '') + ' ' + anchor.selector.split(' > ').slice(-2).join(' > ');
    if (anchor.type === 'file') return '整个页面 ' + page;
    return where + (anchor.quote || '');
  }

  const comments = VinxComments.mount({
    title: '页面批注', badge: commentToggle, describe,
    hint: '点顶栏的「点选批注」，再点页面上的元素或拖出一个区域。',
    query: () => 'prefix=' + encodeURIComponent(prefix()),
    onChange: sendMarkers,
    onFocus: focusItem,
    wholeLabel: '对当前页面写批注',
    wholeTarget: () => selected && framePage ? { target: prefix() + framePage, anchor: { type: 'file' }, label: '整个页面：' + framePage } : null,
  });

  function sendMarkers(all) {
    if (!framePage) return;
    const mine = (all || comments.items).filter(item => item.status !== 'resolved' && item.target === prefix() + framePage
      && (item.anchor.type === 'element' || item.anchor.type === 'region'))
      .map(item => ({ id: item.id, status: item.status, body: item.body, anchor: item.anchor }));
    toFrame({ type: 'markers', items: mine });
  }

  function focusItem(item) {
    const page = item.target.slice(prefix().length);
    if (!item.target.startsWith(prefix())) return;
    if (page !== framePage) { pendingFocus = item.id; framePage = ''; frame.src = '/a/' + selected + '/' + page.split('/').map(encodeURIComponent).join('/'); return; }
    toFrame({ type: 'focus', id: item.id });
  }

  window.addEventListener('message', event => {
    if (event.source !== frame.contentWindow || !event.data || event.data.vinx !== true) return;
    const data = event.data;
    if (data.type === 'ready' && typeof data.page === 'string') {
      framePage = data.page;
      sendMarkers();
      if (EDITABLE.test(framePage) && Array.from(editSelect.options).some(option => option.value === framePage)) editSelect.value = framePage;
      if (pendingFocus) { const id = pendingFocus; pendingFocus = 0; comments.focus(id); setTimeout(() => toFrame({ type: 'focus', id }), 300); }
    } else if (data.type === 'picked' && data.anchor && typeof data.anchor === 'object') {
      pickButton.classList.remove('active');
      const page = typeof data.anchor.page === 'string' ? data.anchor.page : framePage;
      comments.compose(prefix() + page, data.anchor, describe(data.anchor));
    } else if (data.type === 'pick-cancel') {
      pickButton.classList.remove('active');
    } else if (data.type === 'marker' && Number.isInteger(data.id)) {
      comments.focus(data.id);
    }
  });

  pickButton.addEventListener('click', () => {
    const on = !pickButton.classList.contains('active');
    pickButton.classList.toggle('active', on);
    toFrame({ type: 'pick', on });
  });
  commentToggle.addEventListener('click', () => comments.toggle());
  editButton.addEventListener('click', () => {
    const item = items.find(entry => entry.id === selected);
    if (!item || !editSelect.value) return;
    // 停靠在右侧，左边的页面保存后自动刷新，边改边看。
    VinxEditor.open({ target: prefix() + editSelect.value, title: item.title + ' · ' + editSelect.value, dock: true });
  });

  function show() {
    const item = items.find(entry => entry.id === selected);
    frame.hidden = !item; empty.hidden = !!item; openNew.hidden = !item;
    pickButton.hidden = commentToggle.hidden = editButton.hidden = !item;
    currentTitle.textContent = item ? item.title : '';
    followNote.textContent = follow ? '有新页面发布时自动切换到最新一个' : '已固定在所选页面；点第一项恢复跟随最新';
    if (!item) return;
    openNew.href = item.url;
    // 同一次发布只加载一次；内容变化由页面里的自动刷新脚本处理，重新发布才换地址。
    const key = item.id + ':' + item.publishedAt;
    if (key !== shownKey) {
      const switched = !shownKey.startsWith(item.id + ':');
      shownKey = key; framePage = ''; frame.src = item.url;
      const editable = (item.files || []).filter(file => EDITABLE.test(file));
      editSelect.replaceChildren(...editable.map(file => { const option = document.createElement('option'); option.value = option.textContent = file; return option; }));
      editSelect.value = item.entry;
      editSelect.hidden = editable.length < 2; editButton.hidden = !editable.length;
      if (switched) comments.refresh();
    }
    if (location.hash.slice(1) !== item.id) history.replaceState(null, '', location.pathname + location.search + '#' + item.id);
  }

  function render() {
    list.replaceChildren();
    if (!items.length) { list.textContent = '暂无页面。'; return; }
    items.forEach((item, index) => {
      const link = document.createElement('a');
      link.className = 'published-row'; link.href = '#' + item.id;
      if (item.id === selected) link.setAttribute('aria-current', 'page');
      const title = document.createElement('strong'); title.textContent = item.title;
      const meta = document.createElement('span'); meta.className = 'recent-meta';
      meta.textContent = item.root.replace(/^\/home\/[^/]+/, '~') + '/' + item.entry;
      const time = document.createElement('span'); time.className = 'published-time';
      time.textContent = relative(item.publishedAt) + ' 发布' + (item.missing.length ? ' · 缺失 ' + item.missing.length + ' 个文件' : '');
      if (item.missing.length) time.dataset.state = 'warning';
      link.append(title, meta, time);
      link.addEventListener('click', event => {
        event.preventDefault();
        selected = item.id; follow = index === 0;
        render(); show();
      });
      list.append(link);
    });
  }

  async function load() {
    try {
      const response = await fetch('/api/artifacts', { cache: 'no-store', credentials: 'omit' });
      if (!response.ok) throw new Error();
      const next = (await response.json()).artifacts;
      const changed = JSON.stringify(next) !== JSON.stringify(items);
      items = next;
      if (follow || !items.some(item => item.id === selected)) selected = items.length ? items[0].id : '';
      if (changed) render();
      show();
    } catch (_) {
      if (!items.length) list.textContent = '管理接口不可用。请在主机执行 vinx-docs start 后重新加载页面。';
    }
  }

  window.addEventListener('hashchange', () => {
    const id = decodeURIComponent(location.hash.slice(1));
    if (id && id !== selected && items.some(item => item.id === id)) { selected = id; follow = items[0].id === id; render(); show(); }
  });
  DocsifyXTheme.bind(document.getElementById('theme-toggle'));
  load();
  setInterval(() => { if (!document.hidden) load(); }, 3000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
})();
