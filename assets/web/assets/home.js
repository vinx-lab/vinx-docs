(async function () {
  const container = document.getElementById('projects');
  const filter = document.getElementById('project-filter');
  const sync = document.getElementById('sync-docs');
  const form = document.getElementById('register-project');
  const path = document.getElementById('docs-path');
  const register = document.getElementById('register-docs');
  const status = document.getElementById('manage-status');
  const searchForm = document.getElementById('global-search');
  const searchInput = document.getElementById('search-input');
  const searchResults = document.getElementById('search-results');
  const toggleRegister = document.getElementById('toggle-register');
  const registerPanel = document.getElementById('register-panel');
  const SHOWN_RECENT = 3;
  let projects = [];
  let artifacts = [];
  // 未解决批注数，按目标归属汇总：doc:<项目>/… 和 a:<短码>/…
  let openComments = { doc: {}, a: {} };

  const relative = seconds => {
    const gap = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
    if (gap < 90) return '刚刚';
    if (gap < 3600) return Math.floor(gap / 60) + ' 分钟前';
    if (gap < 86400) return Math.floor(gap / 3600) + ' 小时前';
    return Math.floor(gap / 86400) + ' 天前';
  };

  const el = (tag, cls, text) => { const node = document.createElement(tag); if (cls) node.className = cls; if (text !== undefined) node.textContent = text; return node; };
  const withIcon = (node, name) => { node.prepend(VinxIcon(name)); return node; };
  const commentBadge = count => withIcon(el('span', 'comment-badge', count + ' 条待处理批注'), 'comment');

  function recentItem(project, entry) {
    const link = el('a', 'project-recent-row');
    link.href = project.url + '#/' + entry.route;
    link.append(el('span', 'project-recent-title', entry.title), el('span', 'project-recent-time', relative(entry.updatedAt)));
    link.title = entry.path;
    return link;
  }

  function projectCard(project) {
    const card = el('article', 'project-card');
    const head = el('a', 'project-card-head');
    head.href = project.url;
    const titleLine = el('span', 'project-card-title');
    titleLine.append(el('strong', '', project.name), el('span', 'project-id', project.id));
    const detail = el('span', 'project-detail', project.fileCount + ' 份文档与附件 →');
    head.append(titleLine, detail);
    card.append(head);

    const meta = el('div', 'project-card-meta');
    meta.append(el('span', 'project-time', project.updatedAt ? '最近更新 ' + relative(project.updatedAt) : '暂无更新记录'));
    const count = openComments.doc[project.id];
    if (count) { const link = el('a', 'comment-link'); link.href = '/comments.html'; link.title = '在批注页查看'; link.append(commentBadge(count)); meta.append(link); }
    card.append(meta);

    const recent = project.recent || [];
    if (recent.length) {
      const list = el('div', 'project-recent');
      recent.slice(0, SHOWN_RECENT).forEach(entry => list.append(recentItem(project, entry)));
      card.append(list);
      if (recent.length > SHOWN_RECENT) {
        const more = el('button', 'project-recent-more', '再看 ' + (recent.length - SHOWN_RECENT) + ' 条更新');
        more.type = 'button';
        more.addEventListener('click', () => {
          recent.slice(SHOWN_RECENT).forEach(entry => list.append(recentItem(project, entry)));
          more.remove();
        });
        card.append(more);
      }
    }
    return card;
  }

  function render() {
    container.replaceChildren();
    const query = filter.value.toLowerCase();
    const shown = projects.filter(p => (p.name + ' ' + p.id).toLowerCase().includes(query));
    for (const project of shown) container.append(projectCard(project));
    if (!shown.length) container.textContent = projects.length ? '没有匹配的项目。' : '尚无已接入项目，点上方「接入项目」输入文档目录即可开始。';
  }

  function renderPublished() {
    const section = document.getElementById('published-section');
    const box = document.getElementById('published');
    box.replaceChildren();
    section.hidden = !artifacts.length;
    for (const item of artifacts.slice(0, 6)) {
      const card = el('article', 'artifact-card');
      const main = el('a', 'artifact-card-main');
      main.href = '/published.html#' + item.id;
      main.append(el('strong', '', item.title), el('span', 'recent-meta', item.root.replace(/^\/home\/[^/]+/, '~') + '/' + item.entry));
      const foot = el('span', 'artifact-card-foot');
      foot.append(el('span', 'artifact-time', relative(item.publishedAt) + ' 发布'));
      if (item.missing.length) foot.append(withIcon(el('span', 'artifact-warn', '缺失 ' + item.missing.length + ' 个文件'), 'warn'));
      const count = openComments.a[item.id];
      if (count) foot.append(commentBadge(count));
      main.append(foot);
      const open = el('a', 'artifact-open');
      open.append(VinxIcon('external'));
      open.href = item.url; open.target = '_blank'; open.rel = 'noopener';
      open.title = '新窗口打开原页面'; open.setAttribute('aria-label', '新窗口打开「' + item.title + '」');
      card.append(main, open);
      box.append(card);
    }
  }

  async function loadPublished() {
    try {
      const response = await fetch('/api/artifacts', { cache: 'no-store', credentials: 'omit', signal: AbortSignal.timeout(3000) });
      if (!response.ok) return;
      artifacts = (await response.json()).artifacts;
      renderPublished();
    } catch (_) { /* 页面列表只是补充入口，接口不可用时不影响阅读 */ }
  }

  async function loadComments() {
    try {
      const response = await fetch('/api/comments?status=open,sent', { cache: 'no-store', credentials: 'omit', signal: AbortSignal.timeout(3000) });
      if (!response.ok) return;
      const counts = { doc: {}, a: {} };
      for (const item of (await response.json()).comments || []) {
        const match = /^(doc|a):([^/]+)\//.exec(item.target || '');
        if (match) counts[match[1]][match[2]] = (counts[match[1]][match[2]] || 0) + 1;
      }
      openComments = counts;
      render(); renderPublished();
    } catch (_) { /* 批注数只是提示 */ }
  }

  async function loadProjects() {
    const response = await fetch('/catalog.json', { cache: 'no-store' });
    if (!response.ok) throw new Error('项目清单不可用，请同步最新文档。');
    const catalog = await response.json();
    projects = catalog.projects;
    render();
  }

  function message(text, state = '') {
    status.textContent = text;
    status.dataset.state = state;
  }

  function disable(value) {
    sync.disabled = register.disabled = path.disabled = value;
    form.setAttribute('aria-busy', String(value));
  }

  async function operate(endpoint, body) {
    disable(true);
    message(endpoint === '/api/projects' ? '正在接入并同步文档…' : '正在同步所有已接入项目…');
    try {
      let response;
      try {
        response = await fetch(endpoint, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body), cache: 'no-store', credentials: 'omit',
        });
      } catch (_) {
        throw new Error('无法连接管理接口；操作可能仍在进行，请稍后重新同步确认。');
      }
      if (!response.headers.get('content-type')?.includes('application/json')) {
        throw new Error('管理服务暂不可用；操作可能仍在进行，请稍后重新同步确认。');
      }
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || '操作失败，请重试。');
      await loadProjects();
      if (endpoint === '/api/projects') form.reset();
      const time = new Date(result.completedAt).toLocaleString();
      const lines = [`同步完成：${result.projectCount} 个项目，${result.fileCount} 个文件（复用 ${result.reusedCount} 个未变更文件），排除 ${result.skippedCount} 个条目。本次同步：${time}。`];
      if (result.warningCount) {
        lines.push(`⚠ ${result.warningCount} 个已收录文件疑似含明文口令或密钥，可在管理后台确认并排除：${result.warnings.join('、')}${result.warningCount > result.warnings.length ? ' 等' : ''}`);
      }
      message(lines.join('\n'), lines.length > 1 ? 'warning' : 'success');
    } catch (error) {
      message(error.message, 'error');
    } finally {
      disable(false);
    }
  }

  let searchTimer = 0;
  async function runSearch() {
    const text = searchInput.value.trim();
    if (!text) { searchResults.hidden = true; searchResults.replaceChildren(); return; }
    try {
      const hits = await DocsifyXSearch.query(text, { limit: 20 });
      searchResults.replaceChildren();
      searchResults.hidden = false;
      if (!hits.length) { searchResults.textContent = '没有匹配的文档。'; return; }
      for (const hit of hits) {
        const link = document.createElement('a');
        link.className = 'search-row'; link.href = hit.url;
        const title = document.createElement('strong'); title.textContent = hit.title;
        const meta = document.createElement('span'); meta.className = 'search-meta';
        meta.textContent = hit.projectName + ' · ' + hit.path;
        const excerpt = document.createElement('span'); excerpt.className = 'search-excerpt';
        excerpt.textContent = hit.excerpt;
        link.append(title, meta, excerpt); searchResults.append(link);
      }
    } catch (error) {
      searchResults.hidden = false;
      searchResults.textContent = error.message;
    }
  }

  searchInput.addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(runSearch, 160);
  });
  searchForm.addEventListener('submit', event => { event.preventDefault(); runSearch(); });
  filter.addEventListener('input', render);
  toggleRegister.addEventListener('click', () => {
    const open = registerPanel.hidden;
    registerPanel.hidden = !open;
    toggleRegister.setAttribute('aria-expanded', String(open));
    if (open && !path.disabled) path.focus();
  });
  sync.addEventListener('click', () => operate('/api/refresh', {}));
  form.addEventListener('submit', event => {
    event.preventDefault();
    const docsPath = path.value.trim();
    if (!docsPath) { message('请输入文档目录路径。', 'error'); path.focus(); return; }
    operate('/api/projects', { docsPath });
  });

  DocsifyXTheme.bind(document.getElementById('theme-toggle'));

  const accessUrl = document.getElementById('access-url');
  accessUrl.textContent = location.origin + '/';
  try {
    const qr = qrcode(0, 'M');
    qr.addData(location.origin + '/');
    qr.make();
    document.getElementById('qr').innerHTML = qr.createSvgTag({ cellSize: 4, margin: 2, scalable: true });
  } catch (_) {
    document.getElementById('qr').textContent = '二维码生成失败，直接输入上面的地址即可。';
  }

  try { await loadProjects(); } catch (error) { container.textContent = error.message; }
  loadPublished();
  loadComments();
  // 顶栏「批注」上显示未解决数量，一眼知道有没有待办。
  fetch('/api/agents', { cache: 'no-store', credentials: 'omit' }).then(r => r.ok ? r.json() : null).then(info => {
    if (!info) return;
    const active = info.counts.open + info.counts.sent;
    if (active) document.getElementById('comments-link').textContent = '批注 ' + active;
  }).catch(() => {});
  try {
    const response = await fetch('/api/status', { cache: 'no-store', credentials: 'omit', signal: AbortSignal.timeout(3000) });
    if (!response.ok) throw new Error();
    const info = await response.json();
    if (info.service !== 'vinx-docs') throw new Error();
    disable(false);
    if (info.version) document.getElementById('app-version').textContent = 'v' + info.version;
    const mode = info.autoSync?.mode || 'off';
    const auto = info.settings?.autoSync && mode !== 'off';
    message(auto
      ? `自动同步已开启（${mode.startsWith('inotify') ? '文件事件监听' : '定时轮询'}），文档保存后几秒内自动更新。`
      : '自动同步当前关闭，可在管理后台开启，或点“立即同步”。');
    if (info.autoSync?.lastError) message('自动同步上次失败：' + info.autoSync.lastError, 'error');
  } catch (_) {
    message('管理接口不可用，阅读不受影响。请在主机执行 vinx-docs start 后重新加载页面。', 'error');
  }
})();
