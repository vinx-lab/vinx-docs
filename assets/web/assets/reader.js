(async function () {
  const base = location.pathname.replace(/index\.html$/, '').replace(/\/?$/, '/');
  const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
  const loaded = new Map();
  function loadScript(src) {
    if (!loaded.has(src)) {
      loaded.set(src, new Promise((resolve, reject) => {
        const script = document.createElement('script'); script.src = src;
        script.onload = resolve; script.onerror = () => reject(new Error('本地阅读资源加载失败，请检查vendor校验结果。'));
        document.head.append(script);
      }));
    }
    return loaded.get(src);
  }

  DocsifyXTheme.bind(document.getElementById('theme-toggle'));

  const FONT_KEY = 'docsify-x-font';
  const sizes = ['', 'large', 'larger'];
  function applyFont(value) {
    document.body.dataset.font = value || '';
  }
  try { applyFont(localStorage.getItem(FONT_KEY) || ''); } catch (_) { /* 隐私模式忽略 */ }
  document.getElementById('font-toggle').addEventListener('click', () => {
    const next = sizes[(sizes.indexOf(document.body.dataset.font || '') + 1) % sizes.length];
    applyFont(next);
    try { localStorage.setItem(FONT_KEY, next); } catch (_) { /* 同上 */ }
  });

  // 桌面宽度下记住侧栏收起状态；手机宽度 close 语义相反（表示展开），不记录也不恢复。
  const SIDEBAR_KEY = 'docsify-x-sidebar';
  const narrow = () => matchMedia('(max-width: 768px)').matches;
  try { if (!narrow() && localStorage.getItem(SIDEBAR_KEY) === 'closed') document.body.classList.add('close'); } catch (_) { /* 隐私模式忽略 */ }
  new MutationObserver(() => {
    if (narrow()) return;
    try { localStorage.setItem(SIDEBAR_KEY, document.body.classList.contains('close') ? 'closed' : 'open'); } catch (_) { /* 同上 */ }
  }).observe(document.body, { attributes: true, attributeFilter: ['class'] });

  // 后台同步后只提示，不自动刷新，避免打断正在阅读的页面。
  function watchUpdates(url, version) {
    const notice = document.getElementById('update-notice');
    let dismissed = '';
    async function check() {
      if (document.hidden) return;
      try {
        const response = await fetch(url, { cache: 'no-store' });
        if (!response.ok) return;
        const latest = (await response.json()).version;
        if (latest && latest !== version && latest !== dismissed) { notice.dataset.version = latest; notice.hidden = false; }
      } catch (_) { /* 同步写入中或服务暂不可达，下次再查 */ }
    }
    document.getElementById('update-reload').addEventListener('click', () => { rememberPosition(); location.reload(); });
    document.getElementById('update-dismiss').addEventListener('click', () => { dismissed = notice.dataset.version || ''; notice.hidden = true; });
    setInterval(check, 15000);
    document.addEventListener('visibilitychange', check);
  }

  // 点“刷新查看”前记下当前小节标题及其相对视口的位置；更新后内容长度可能变化，按标题定位比按像素准。
  const RESTORE_KEY = 'docsify-x-restore';
  function rememberPosition() {
    const top = document.querySelector('.reader-top')?.getBoundingClientRect().bottom || 0;
    const headings = Array.from(document.querySelectorAll('.markdown-section :is(h1,h2,h3,h4,h5,h6)[id]'));
    let anchor = null;
    for (const heading of headings) { if (heading.getBoundingClientRect().top <= top + 8) anchor = heading; else break; }
    const state = { hash: location.hash, y: scrollY, at: Date.now(), id: anchor?.id || '', rel: anchor ? anchor.getBoundingClientRect().top : 0 };
    try { sessionStorage.setItem(RESTORE_KEY, JSON.stringify(state)); } catch (_) { /* 隐私模式忽略 */ }
  }
  function restorePosition() {
    let state = null;
    try { state = JSON.parse(sessionStorage.getItem(RESTORE_KEY) || 'null'); sessionStorage.removeItem(RESTORE_KEY); } catch (_) { return; }
    if (!state || state.hash !== location.hash || Date.now() - state.at > 60000) return;
    const anchor = state.id && document.getElementById(state.id);
    const y = anchor ? anchor.getBoundingClientRect().top + scrollY - state.rel : state.y;
    // 等 Docsify 自身的回顶滚动结束后再定位。
    setTimeout(() => window.scrollTo(0, Math.max(0, y)), 0);
  }

  // 代码块复制：主机IP访问是非安全上下文，没有 navigator.clipboard，退回 execCommand。
  async function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    const area = document.createElement('textarea');
    area.value = text; area.setAttribute('readonly', ''); area.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
    document.body.append(area); area.select();
    try { if (!document.execCommand('copy')) throw new Error('copy failed'); } finally { area.remove(); }
  }
  function addCopyButtons(section) {
    section.querySelectorAll('pre').forEach(pre => {
      if (pre.parentElement.classList.contains('code-block')) return;
      const wrapper = document.createElement('div'); wrapper.className = 'code-block';
      pre.before(wrapper); wrapper.append(pre);
      const button = document.createElement('button');
      button.type = 'button'; button.className = 'copy-code'; button.textContent = '复制';
      button.setAttribute('aria-label', '复制代码');
      let timer = 0;
      button.addEventListener('click', async () => {
        clearTimeout(timer);
        try { await copyText((pre.querySelector('code') || pre).innerText.replace(/\n$/, '')); button.textContent = '已复制'; button.dataset.state = 'done'; }
        catch (_) { button.textContent = '复制失败'; button.dataset.state = 'error'; }
        timer = setTimeout(() => { button.textContent = '复制'; delete button.dataset.state; }, 1600);
      });
      wrapper.append(button);
    });
  }

  // GitHub Alerts：`> [!NOTE]` 等五种引用块提示，渲染为带图标的提示框（图标为静态SVG常量）。
  const ALERT_ICON = {
    note: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
    tip: '<path d="M9 18h6M10 21h4M12 3a6 6 0 0 0-3.5 10.9c.6.5 1 1.2 1 2.1h5c0-.9.4-1.6 1-2.1A6 6 0 0 0 12 3z"/>',
    important: '<path d="M4 5h16v11H9l-5 4z"/><path d="M12 8v3M12 13.5h.01"/>',
    warning: '<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>',
    caution: '<path d="M8 3h8l5 5v8l-5 5H8l-5-5V8z"/><path d="M12 8v5M12 16h.01"/>'
  };
  const ALERT_LABEL = { note: '说明', tip: '提示', important: '重要', warning: '警告', caution: '注意' };
  function renderAlerts(section) {
    section.querySelectorAll('blockquote:not(.alert)').forEach(quote => {
      const first = quote.firstElementChild;
      const text = first && first.tagName === 'P' ? first.firstChild : null;
      const match = text && text.nodeType === Node.TEXT_NODE && text.nodeValue.match(/^\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/i);
      if (!match) return;
      text.nodeValue = text.nodeValue.slice(match[0].length);
      if (first.firstChild && first.firstChild.nodeName === 'BR') first.firstChild.remove();
      if (!first.textContent.trim() && !first.querySelector('img')) first.remove();
      const type = match[1].toLowerCase();
      quote.classList.add('alert', 'alert-' + type);
      // 只有一段文字时标题与正文同行，省一行；列表等其他内容标题单独一行。
      const single = quote.children.length === 1 && quote.firstElementChild.tagName === 'P';
      const title = document.createElement(single ? 'span' : 'p');
      title.className = 'alert-title';
      title.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true">' + ALERT_ICON[type] + '</svg>';
      title.append(ALERT_LABEL[type]);
      if (single) { quote.classList.add('alert-inline'); quote.firstElementChild.prepend(title); }
      else quote.prepend(title);
    });
  }

  async function renderDiagrams() {
    const nodes = Array.from(document.querySelectorAll('.markdown-section .mermaid:not([data-rendered])'));
    if (!nodes.length) return;
    try {
      await loadScript('/vendor/mermaid.min.js');
      const dark = DocsifyXTheme.current() === 'dark';
      mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: dark ? 'dark' : 'default' });
      for (const node of nodes) node.setAttribute('data-rendered', '1');
      await mermaid.run({ nodes });
    } catch (error) {
      for (const node of nodes) {
        node.setAttribute('data-rendered', '1');
        node.classList.add('mermaid-failed');
        node.textContent = '图表渲染失败：' + error.message;
      }
    }
  }

  function buildToc(section) {
    document.querySelector('.page-toc')?.remove();
    const headings = Array.from(section.querySelectorAll('h2, h3')).filter(node => node.id);
    if (headings.length < 3) return;
    const nav = document.createElement('nav');
    nav.className = 'page-toc'; nav.setAttribute('aria-label', '本页目录');
    const title = document.createElement('p'); title.textContent = '本页目录';
    const list = document.createElement('ul');
    for (const heading of headings) {
      const item = document.createElement('li');
      item.className = heading.tagName === 'H3' ? 'toc-sub' : '';
      const link = document.createElement('a');
      link.href = '#' + heading.id;
      link.textContent = heading.textContent.replace(/^\s*#\s*/, '');
      item.append(link); list.append(item);
    }
    nav.append(title, list);
    section.prepend(nav);
  }

  function buildPager(section, meta, route) {
    const ordered = meta.entries.filter(entry => entry.route);
    const at = ordered.findIndex(entry => entry.route === route);
    if (at < 0) return;
    const pager = document.createElement('nav');
    pager.className = 'pager'; pager.setAttribute('aria-label', '上一篇下一篇');
    for (const [offset, mark] of [[-1, '← 上一篇'], [1, '下一篇 →']]) {
      const target = ordered[at + offset];
      if (!target) continue;
      const link = document.createElement('a');
      link.className = 'pager-link' + (offset < 0 ? ' pager-prev' : ' pager-next');
      link.href = '#/' + target.route;
      const mark_ = document.createElement('small'); mark_.textContent = mark;
      const title = document.createElement('span'); title.textContent = target.title;
      link.append(mark_, title); pager.append(link);
    }
    if (pager.children.length) section.append(pager);
  }

  try {
    const response = await fetch(base + 'manifest.json', { cache: 'no-store' });
    if (!response.ok) throw new Error('未找到有效项目清单，请检查注册和刷新结果。');
    const meta = await response.json();
    document.title = meta.name + ' · Vinx Docs';
    document.getElementById('project-label').textContent = meta.name;
    watchUpdates(base + 'manifest.json', meta.version);
    let source = meta.home;

    const searchInput = document.getElementById('reader-search-input');
    const searchResults = document.getElementById('reader-search-results');
    let searchTimer = 0;
    async function runSearch() {
      const text = searchInput.value.trim();
      if (!text) { searchResults.hidden = true; searchResults.replaceChildren(); return; }
      try {
        const hits = await DocsifyXSearch.query(text, { limit: 15 });
        searchResults.replaceChildren();
        searchResults.hidden = false;
        if (!hits.length) { searchResults.textContent = '没有匹配的文档。'; return; }
        for (const hit of hits) {
          const link = document.createElement('a');
          link.className = 'search-row'; link.href = hit.url;
          const title = document.createElement('strong'); title.textContent = hit.title;
          const label = document.createElement('span'); label.className = 'search-meta';
          label.textContent = (hit.project === meta.id ? '本项目' : hit.projectName) + ' · ' + hit.path;
          link.append(title, label); searchResults.append(link);
        }
      } catch (error) {
        searchResults.hidden = false;
        searchResults.textContent = error.message;
      }
    }
    searchInput.addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(runSearch, 160); });
    document.getElementById('reader-search').addEventListener('submit', event => { event.preventDefault(); runSearch(); });
    document.addEventListener('click', event => {
      if (!searchResults.hidden && !searchResults.contains(event.target) && event.target !== searchInput) searchResults.hidden = true;
    });

    window.$docsify = {
      // 项目名指向本项目首页：进了项目就是一个独立空间，返回主站走顶栏的“文档中心”。
      name: escape(meta.name), nameLink: '#/', basePath: base + 'content/', homepage: meta.home,
      loadSidebar: true, alias: { '/.*/_sidebar.md': '/_sidebar.md' },
      subMaxLevel: 3, auto2top: true, executeScript: false, externalLinkTarget: '_blank',
      notFoundPage: false, themeColor: '#176b79',
      search: { paths: meta.entries.filter(e => e.route).map(e => '/' + e.route), namespace: 'docsify-x-' + meta.id + '-' + meta.version, placeholder: '搜索当前项目', noData: '未找到匹配内容', depth: 4, maxAge: 86400000 },
      plugins: [function (hook, vm) {
        let route = meta.home;
        // 编辑和批注（reader-vinx.js）通过这里拿到当前文档和 Docsify 的编译器。
        window.DocsifyXReader = { meta, vm, source: () => source, route: () => route,
          entry: () => meta.entries.find(e => e.path === source) || null };
        hook.beforeEach(function (content) {
          try { route = decodeURIComponent(vm.route.path).replace(/^\//, '') || meta.home; } catch (_) { route = meta.home; }
          const entry = meta.entries.find(e => e.route === route || e.route === route + '.md');
          source = entry ? entry.path : meta.home;
          return content;
        });
        hook.doneEach(function () {
          const section = document.querySelector('.markdown-section');
          if (!section) return;
          section.querySelectorAll('table').forEach(table => {
            if (table.parentElement.classList.contains('table-scroll')) return;
            const wrapper = document.createElement('div'); wrapper.className = 'table-scroll';
            table.before(wrapper); wrapper.append(table);
          });
          buildToc(section);
          buildPager(section, meta, route);
          renderAlerts(section);
          addCopyButtons(section);
          renderDiagrams().then(restorePosition);
          document.dispatchEvent(new CustomEvent('docsifyx:rendered', { detail: { section, source, route } }));
        });
      }],
      markdown: { renderer: {
        code: function (code, language) {
          // Mermaid 代码块交给本地 mermaid 渲染；其余语言仍走 Docsify 默认高亮。
          if ((language || '').toLowerCase() === 'mermaid') {
            return '<div class="mermaid">' + escape(code) + '</div>';
          }
          return this.origin.code.apply(this, arguments);
        },
        link: function (href, title, text) {
          const result = DocsifyXLinks.resolveLink(href, source, meta);
          if (result.type === 'blocked') return '<span class="unpublished" title="目标未纳入允许清单：' + escape(href) + '">' + text + '<small> 未收录</small></span>';
          const attrs = result.type === 'document' ? '' : ' data-docsify-ignore target="_blank" rel="noopener noreferrer"';
          const download = ['download', 'image'].includes(result.type) ? ' download' : '';
          const label = result.type === 'html' ? ' <small>↗ HTML</small>'
            : result.type === 'sheet' ? ' <small>▦ 表格预览</small>'
            : result.type === 'download' ? ' <small>↓ 原件</small>' : '';
          return '<a href="' + escape(result.href) + '"' + attrs + download + (title ? ' title="' + escape(title) + '"' : '') + '>' + text + label + '</a>';
        },
        image: function (href, title, text) {
          const result = DocsifyXLinks.resolveLink(href, source, meta);
          if (result.type !== 'image') return '<span class="unpublished">[未收录图片：' + escape(text || href) + ']</span>';
          return '<img src="' + escape(result.href) + '" alt="' + escape(text) + '" loading="lazy">';
        }
      } }
    };
    // 搜索插件先注册，再启动Docsify，避免异步加载晚于其生命周期初始化。
    await loadScript('/vendor/search.min.js');
    await loadScript('/vendor/docsify.min.js');
  } catch (error) {
    const app = document.getElementById('app');
    if (app) app.textContent = error.message;
  }
})();
