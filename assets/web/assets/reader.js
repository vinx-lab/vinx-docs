(async function () {
  const base = location.pathname.replace(/index\.html$/, '').replace(/\/?$/, '/');
  // read.html?a=<短码>：用短链接发布的单个 Markdown，清单和内容都从短链接取。
  const shortId = new URLSearchParams(location.search).get('a');
  if (shortId) document.body.classList.add('single-doc');
  const manifestUrl = shortId ? '/a/' + encodeURIComponent(shortId) + '/__docsify_x/manifest' : base + 'manifest.json';
  const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
  // marked 交给 link 渲染器的 title 已经转义过一次，先还原再转义，避免悬停提示里出现 &quot;。
  const unescapeHtml = value => String(value).replace(/&(amp|lt|gt|quot|#39);/g, (_, name) => ({ amp: '&', lt: '<', gt: '>', quot: '"', '#39': "'" }[name]));
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

  // 顶栏在手机宽度下会换行变高（按钮和搜索框各占一行，字号、项目名长短都有影响）。
  // 把顶栏实际高度写进 --reader-top-h，侧栏、正文和搜索结果据此让开顶栏，不靠猜测的固定值。
  const readerTop = document.querySelector('.reader-top');
  if (readerTop) {
    const syncTopHeight = () => document.documentElement.style.setProperty('--reader-top-h', Math.ceil(readerTop.getBoundingClientRect().height) + 'px');
    syncTopHeight();
    if (window.ResizeObserver) new ResizeObserver(syncTopHeight).observe(readerTop);
    else addEventListener('resize', syncTopHeight);
  }

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

  // 本页目录。链接用 Docsify 给标题生成的锚点（#/<路由>?id=<标题>），写成 #<标题id> 会被当成路由跳到 404。
  // 项目阅读页：浮在正文右上角。单文件阅读页（短链接）没有侧栏：固定在左侧、垂直居中、随滚动高亮当前小节，可以收起。
  const TOC_KEY = 'docsify-x-toc';
  const tocNarrow = () => matchMedia('(max-width: 900px)').matches;
  let tocSpy = null, tocWidthWatched = false;
  function setTocOpen(open, remember) {
    document.body.classList.toggle('toc-open', open);
    const toggle = document.querySelector('.toc-toggle');
    if (toggle) { toggle.setAttribute('aria-expanded', String(open)); toggle.hidden = open; }
    if (remember && !tocNarrow()) { try { localStorage.setItem(TOC_KEY, open ? 'open' : 'closed'); } catch (_) { /* 隐私模式忽略 */ } }
  }
  function tocToggle() {
    let toggle = document.querySelector('.toc-toggle');
    if (toggle) return toggle;
    toggle = document.createElement('button');
    toggle.type = 'button'; toggle.className = 'toc-toggle'; toggle.textContent = '目录';
    toggle.title = '显示本页目录'; toggle.setAttribute('aria-label', '显示本页目录');
    toggle.addEventListener('click', () => setTocOpen(true, true));
    document.body.append(toggle);
    return toggle;
  }
  function buildToc(section, route) {
    document.querySelector('.page-toc')?.remove();
    if (tocSpy) { removeEventListener('scroll', tocSpy); tocSpy = null; }
    const side = document.body.classList.contains('single-doc');
    // 左侧目录收二到四级标题（h2–h4），按层级成树、可折叠；项目阅读页右上角的目录只列 h2、h3。
    const headings = Array.from(section.querySelectorAll(side ? 'h2, h3, h4' : 'h2, h3')).filter(node => node.id);
    if (headings.length < 3) {
      document.body.classList.remove('has-toc', 'toc-open');
      document.querySelector('.toc-toggle')?.remove();
      return;
    }
    const nav = document.createElement('nav');
    nav.className = 'page-toc' + (side ? ' page-toc-side' : ''); nav.setAttribute('aria-label', '本页目录');
    if (side) nav.id = 'page-toc';
    const head = document.createElement('div'); head.className = 'page-toc-head';
    const title = document.createElement('p'); title.textContent = '本页目录';
    head.append(title);
    const list = document.createElement('ul'); list.className = 'toc-list';
    let onJump = () => {};
    function makeLink(heading) {
      const link = document.createElement('a');
      link.href = heading.querySelector('a[href*="?id="]')?.getAttribute('href') || '#/' + encodeURI(route) + '?id=' + encodeURIComponent(heading.id);
      link.textContent = heading.textContent.replace(/^\s*#\s*/, '');
      // 自己滚动（标题的 scroll-margin-top 让开固定顶栏），只改地址不触发 hashchange：Docsify 的滚动会把标题顶到顶栏下面。
      link.addEventListener('click', event => {
        if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        history.replaceState(history.state, '', link.href);
        onJump(heading);
        heading.scrollIntoView({ block: 'start', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
        if (side && tocNarrow()) setTocOpen(false, false);
      });
      return link;
    }
    nav.append(head, list);
    if (!side) {
      for (const heading of headings) {
        const item = document.createElement('li');
        item.className = heading.tagName === 'H3' ? 'toc-sub' : '';
        item.append(makeLink(heading)); list.append(item);
      }
      section.prepend(nav);
      return;
    }

    // 树：每个标题挂在它前面最近的、级别更高的标题下面；有子项的节点前面是折叠按钮。
    // auto：跟随滚动自动展开的分支，离开时再收起；手动点过的分支不再自动收起。
    const links = [], nodes = [], parents = new Map(), foldable = [], auto = new Set();
    const setExpanded = (node, open) => {
      node.classList.toggle('collapsed', !open);
      node.querySelector(':scope > .toc-row > .toc-fold')?.setAttribute('aria-expanded', String(open));
    };
    const level = heading => Number(heading.tagName[1]);
    const stack = [{ level: 0, ul: list, node: null }];
    for (const heading of headings) {
      while (stack.length > 1 && stack[stack.length - 1].level >= level(heading)) stack.pop();
      const parent = stack[stack.length - 1];
      if (!parent.ul) {
        parent.ul = document.createElement('ul'); parent.ul.className = 'toc-children';
        parent.node.append(parent.ul);
        const fold = document.createElement('button');
        fold.type = 'button'; fold.className = 'toc-fold';
        fold.setAttribute('aria-label', '展开或折叠「' + parent.node.querySelector('a').textContent + '」');
        const node = parent.node;
        fold.addEventListener('click', () => { auto.delete(node); setExpanded(node, node.classList.contains('collapsed')); });
        node.querySelector('.toc-fold-spacer').replaceWith(fold);
        foldable.push(node);
      }
      const node = document.createElement('li'); node.className = 'toc-node';
      const row = document.createElement('div'); row.className = 'toc-row';
      const spacer = document.createElement('span'); spacer.className = 'toc-fold-spacer'; spacer.setAttribute('aria-hidden', 'true');
      const link = makeLink(heading);
      row.append(spacer, link); node.append(row); parent.ul.append(node);
      parents.set(node, parent.node);
      stack.push({ level: level(heading), ul: null, node });
      links.push(link); nodes.push(node);
    }
    // 默认只展开当前小节所在的分支；标题不多（12 个以内）时全部展开。
    foldable.forEach(node => setExpanded(node, headings.length <= 12));
    const tools = document.createElement('span'); tools.className = 'page-toc-tools';
    if (foldable.length) for (const [text, open] of [['全部展开', true], ['全部折叠', false]]) {
      const button = document.createElement('button');
      button.type = 'button'; button.textContent = text;
      button.addEventListener('click', () => { auto.clear(); foldable.forEach(node => setExpanded(node, open)); });
      tools.append(button);
    }

    const hide = document.createElement('button');
    hide.type = 'button'; hide.className = 'page-toc-hide'; hide.textContent = '×';
    hide.title = '隐藏目录'; hide.setAttribute('aria-label', '隐藏目录');
    hide.addEventListener('click', () => { setTocOpen(false, true); tocToggle().focus(); });
    tools.append(hide);
    head.append(tools);
    document.body.append(nav);
    document.body.classList.add('has-toc');
    tocToggle().setAttribute('aria-controls', 'page-toc');
    // 宽屏按上次的选择（默认展开）；窄屏默认收起，展开时浮在正文上，点目录项后自动收起。
    const restore = () => {
      let saved = '';
      try { saved = localStorage.getItem(TOC_KEY) || ''; } catch (_) { /* 同上 */ }
      setTocOpen(tocNarrow() ? false : saved !== 'closed', false);
    };
    restore();
    if (!tocWidthWatched) { tocWidthWatched = true; matchMedia('(max-width: 900px)').addEventListener('change', () => { if (document.body.classList.contains('has-toc')) restore(); }); }

    // 当前小节：最后一个越过顶栏下沿的标题。换到新小节时展开它所在的分支（不收起别的），并让它保持在目录可视范围内。
    let current = -1, frame = 0, jumping = 0;
    // 点目录项：有子项的展开并保持（算手动展开）；平滑滚动途中不跟随，免得沿途的分支开了又关，到位后再更新。
    onJump = heading => {
      const node = nodes[headings.indexOf(heading)];
      if (node && foldable.includes(node)) { auto.delete(node); setExpanded(node, true); }
      clearTimeout(jumping);
      const done = () => { clearTimeout(jumping); jumping = 0; removeEventListener('scrollend', done); tocSpy(); };
      jumping = setTimeout(done, 1200);
      addEventListener('scrollend', done);
    };
    tocSpy = () => {
      if (frame || jumping) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        const top = (document.querySelector('.reader-top')?.getBoundingClientRect().bottom || 0) + 24;
        let at = 0;
        headings.forEach((heading, index) => { if (heading.getBoundingClientRect().top <= top) at = index; });
        if (innerHeight + scrollY >= document.documentElement.scrollHeight - 2) at = headings.length - 1;
        if (at === current) return;
        if (current >= 0) links[current].removeAttribute('aria-current');
        nav.querySelectorAll('.toc-node.has-current').forEach(node => node.classList.remove('has-current'));
        current = at; links[at].setAttribute('aria-current', 'location');
        // 当前分支：当前小节自己（有子项时）和它的各级上级。
        const branch = new Set();
        if (foldable.includes(nodes[at])) branch.add(nodes[at]);
        for (let node = parents.get(nodes[at]); node; node = parents.get(node)) branch.add(node);
        for (const node of auto) if (!branch.has(node)) { setExpanded(node, false); auto.delete(node); }
        for (const node of branch) {
          if (node !== nodes[at]) node.classList.add('has-current');
          if (node.classList.contains('collapsed')) { setExpanded(node, true); auto.add(node); }
        }
        const box = list.getBoundingClientRect(), row = links[at].getBoundingClientRect();
        if (row.top < box.top || row.bottom > box.bottom) list.scrollTop += row.top - box.top - box.height / 2;
      });
    };
    addEventListener('scroll', tocSpy, { passive: true });
    tocSpy();
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

  // 图片放大查看：先按屏幕大小完整显示，再点一下看原始尺寸（可滚动）；点空白处或按 Esc 关闭。
  function zoomImage(src, alt) {
    const overlay = document.createElement('div');
    overlay.className = 'zoom-overlay'; overlay.setAttribute('role', 'dialog'); overlay.setAttribute('aria-label', '图片放大查看');
    const img = document.createElement('img'); img.src = src; img.alt = alt || ''; img.title = '点击切换原始尺寸';
    const close = document.createElement('button'); close.type = 'button'; close.className = 'zoom-close'; close.textContent = '×'; close.setAttribute('aria-label', '关闭');
    overlay.append(img, close);
    const done = () => { overlay.remove(); removeEventListener('keydown', onKey); };
    const onKey = event => { if (event.key === 'Escape') done(); };
    img.addEventListener('click', event => { event.stopPropagation(); overlay.classList.toggle('actual'); });
    overlay.addEventListener('click', done);
    addEventListener('keydown', onKey);
    document.body.append(overlay);
    close.focus();
  }

  try {
    const response = await fetch(manifestUrl, { cache: 'no-store' });
    if (!response.ok) throw new Error('未找到有效项目清单，请检查注册和刷新结果。');
    const meta = await response.json();
    document.title = meta.name + ' · Vinx Docs';
    document.getElementById('project-label').textContent = meta.name;
    watchUpdates(meta.versionUrl || manifestUrl, meta.version);
    // 短链接：下载源文件（单个文件原样下载，多个文件打成 zip）。
    if (shortId && window.VinxDownloadMenu) VinxDownloadMenu.set(document.getElementById('download-menu'), meta.downloadUrl, meta.downloadNames);
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
      name: escape(meta.name), nameLink: '#/', basePath: meta.content || base + 'content/', homepage: meta.home,
      loadSidebar: !shortId, alias: { '/.*/_sidebar.md': '/_sidebar.md' },
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
          // 媒体入口的正文是生成的，source 指向媒体文件本身。
          source = entry ? entry.source || entry.path : meta.home;
          // 开头的 front matter（--- 包起来的 YAML）显示成代码块，不被当成分隔线和标题。
          return content.replace(/^---\r?\n([\s\S]*?)\r?\n---\r?\n/, (_, yaml) => '```yaml\n' + yaml + '\n```\n\n');
        });
        hook.doneEach(function () {
          const section = document.querySelector('.markdown-section');
          if (!section) return;
          // 图片和音视频作入口时的页面：媒体按屏幕高度等比缩小，一屏看完；图片点击放大，视频用播放器自带的全屏。
          const mediaView = meta.entries.some(e => e.route === route && e.source);
          section.classList.toggle('media-view', mediaView);
          if (mediaView) section.querySelectorAll('img').forEach(img => {
            img.title = '点击放大';
            img.addEventListener('click', () => zoomImage(img.src, img.alt));
          });
          // Markdown 里直接写的 <img>/<video>/<audio>/<source> 用的是相对源文件的地址，按清单换成可访问的地址。
          section.querySelectorAll('img[src], video[src], video[poster], audio[src], source[src]').forEach(node => {
            ['src', 'poster'].forEach(attr => {
              const value = node.getAttribute(attr);
              if (!value || /^(data:|blob:|\/)/i.test(value)) return;
              const result = DocsifyXLinks.resolveLink(value, source, meta);
              if (['image', 'video', 'audio'].includes(result.type)) node.setAttribute(attr, result.href);
            });
          });
          section.querySelectorAll('table').forEach(table => {
            if (table.parentElement.classList.contains('table-scroll')) return;
            const wrapper = document.createElement('div'); wrapper.className = 'table-scroll';
            table.before(wrapper); wrapper.append(table);
          });
          buildToc(section, route);
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
            : result.type === 'video' ? ' <small>▶ 视频</small>'
            : result.type === 'audio' ? ' <small>▶ 音频</small>'
            : result.type === 'download' ? ' <small>↓ 原件</small>' : '';
          return '<a href="' + escape(result.href) + '"' + attrs + download + (title ? ' title="' + escape(unescapeHtml(title)) + '"' : '') + '>' + text + label + '</a>';
        },
        image: function (href, title, text) {
          const result = DocsifyXLinks.resolveLink(href, source, meta);
          // 图片语法引用视频或音频时内嵌播放器；只加载元数据，点播放才拉取内容。
          const label = escape(text || title || '');
          if (result.type === 'video') return '<video controls preload="metadata" src="' + escape(result.href) + '" aria-label="' + label + '"></video>';
          if (result.type === 'audio') return '<audio controls preload="metadata" src="' + escape(result.href) + '" aria-label="' + label + '"></audio>';
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
