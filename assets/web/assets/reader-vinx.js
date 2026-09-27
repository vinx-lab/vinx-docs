/* 阅读页的编辑与批注：选中正文文字即可批注，已有批注在正文里高亮；「编辑」打开源文件编辑器。
   reader.js 每次渲染完发出 docsifyx:rendered，这里据此重画高亮。 */
(function () {
  const SKIP = '.page-toc, .pager, .copy-code, .vinx-select-btn, script, style';
  let comments = null;
  let section = null;
  const floating = document.createElement('button');
  floating.type = 'button'; floating.className = 'vinx-select-btn'; floating.textContent = '批注'; floating.hidden = true;
  document.body.append(floating);
  // 按下按钮时不让浏览器清掉选区、抢走焦点，点击后焦点才能稳定落到批注输入框。
  floating.addEventListener('mousedown', event => event.preventDefault());

  const reader = () => window.DocsifyXReader;
  const target = () => (reader().meta.target || 'doc:' + reader().meta.id + '/') + reader().source();

  // 正文里的文字节点及其在整段文字里的起点；批注的原文、上下文都按这个坐标算。
  function textIndex(root) {
    const nodes = [];
    let full = '';
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode: node => node.parentElement.closest(SKIP) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
    });
    while (walker.nextNode()) { nodes.push({ node: walker.currentNode, start: full.length }); full += walker.currentNode.nodeValue; }
    return { nodes, full };
  }

  function score(full, at, length, prefix, suffix) {
    let s = 0;
    while (s < prefix.length && at - 1 - s >= 0 && full[at - 1 - s] === prefix[prefix.length - 1 - s]) s++;
    let t = 0;
    while (t < suffix.length && full[at + length + t] === suffix[t]) t++;
    return s + t;
  }

  function findQuote(full, anchor) {
    const quote = anchor.quote || '';
    if (!quote) return null;
    let best = -1, bestScore = -1;
    for (let at = full.indexOf(quote); at >= 0; at = full.indexOf(quote, at + 1)) {
      const s = score(full, at, quote.length, anchor.prefix || '', anchor.suffix || '');
      if (s > bestScore) { best = at; bestScore = s; }
    }
    return best < 0 ? null : [best, best + quote.length];
  }

  function unwrapMarks(root) {
    root.querySelectorAll('mark.vinx-mark').forEach(mark => mark.replaceWith(...mark.childNodes));
    root.normalize();
  }

  function highlight(items) {
    if (!section || !section.isConnected) return;
    unwrapMarks(section);
    const lost = [];
    for (const item of items) {
      if (item.status === 'resolved' || item.anchor.type !== 'text') continue;
      const { nodes, full } = textIndex(section);
      const span = findQuote(full, item.anchor);
      if (!span) { lost.push(item.id); continue; }
      const pieces = [];
      for (const { node, start } of nodes) {
        const end = start + node.nodeValue.length;
        if (end <= span[0] || start >= span[1]) continue;
        pieces.push([node, Math.max(0, span[0] - start), Math.min(node.nodeValue.length, span[1] - start)]);
      }
      for (const [node, from, to] of pieces) {
        const range = document.createRange();
        range.setStart(node, from); range.setEnd(node, to);
        const mark = document.createElement('mark');
        mark.className = 'vinx-mark'; mark.dataset.id = item.id; mark.dataset.status = item.status;
        mark.title = '批注 #' + item.id + '：' + item.body;
        try { range.surroundContents(mark); } catch (_) { /* 跨元素的片段跳过这一段 */ }
      }
    }
    section.dataset.lostComments = lost.join(',');
  }

  function headingBefore(node) {
    let found = '';
    for (const heading of section.querySelectorAll('h1, h2, h3, h4')) {
      if (heading.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING) found = heading.textContent.replace(/^\s*#\s*/, '').trim();
      else break;
    }
    return found;
  }

  function selectionAnchor() {
    const selection = getSelection();
    if (!section || !selection.rangeCount || selection.isCollapsed) return null;
    const range = selection.getRangeAt(0);
    if (!section.contains(range.commonAncestorContainer)) return null;
    const raw = selection.toString();
    const quote = raw.trim();
    if (!quote || quote.length > 600) return null;
    const { nodes, full } = textIndex(section);
    const hit = nodes.find(entry => entry.node === range.startContainer);
    let start = hit ? hit.start + range.startOffset : full.indexOf(quote);
    start += raw.length - raw.trimStart().length;
    if (start < 0) start = 0;
    const end = start + quote.length;
    return {
      type: 'text', quote,
      prefix: full.slice(Math.max(0, start - 64), start),
      suffix: full.slice(end, end + 64),
      heading: headingBefore(range.startContainer),
    };
  }

  function showFloating() {
    const anchor = selectionAnchor();
    if (!anchor) { floating.hidden = true; return; }
    const rect = getSelection().getRangeAt(0).getBoundingClientRect();
    floating.style.top = Math.max(60, rect.top + scrollY - 40) + 'px';
    floating.style.left = Math.min(innerWidth - 80, rect.right + scrollX - 30) + 'px';
    floating.hidden = false;
    floating.onclick = () => {
      floating.hidden = true;
      comments.compose(target(), anchor, '“' + anchor.quote + '”');
      getSelection().removeAllRanges();
    };
  }
  document.addEventListener('mouseup', event => { if (event.target !== floating) setTimeout(showFloating, 0); });
  document.addEventListener('keyup', event => { if (event.shiftKey) showFloating(); });
  document.addEventListener('touchend', () => setTimeout(showFloating, 250));

  document.addEventListener('click', event => {
    const mark = event.target.closest && event.target.closest('mark.vinx-mark');
    if (mark && comments) comments.focus(Number(mark.dataset.id));
  });

  function flash(id) {
    const mark = section && section.querySelector(`mark.vinx-mark[data-id="${id}"]`);
    if (!mark) return;
    mark.scrollIntoView({ block: 'center', behavior: 'smooth' });
    mark.classList.add('flash'); setTimeout(() => mark.classList.remove('flash'), 1600);
  }

  function describe(anchor) {
    if (anchor.type === 'text') return (anchor.heading ? anchor.heading + ' · ' : '') + '“' + anchor.quote + '”';
    if (anchor.type === 'file') return '整篇文档';
    return anchor.selector || '';
  }

  document.addEventListener('docsifyx:rendered', event => {
    section = event.detail.section;
    floating.hidden = true;
    if (!comments) {
      const toggle = document.getElementById('comment-toggle');
      comments = VinxComments.mount({
        title: '本页批注', badge: toggle, describe,
        hint: '选中正文里的文字，点浮出的「批注」即可。',
        query: () => 'target=' + encodeURIComponent(target()),
        onChange: highlight,
        onFocus: item => flash(item.id),
        wholeLabel: '对整篇文档写批注',
        wholeTarget: () => ({ target: target(), anchor: { type: 'file' }, label: '整篇文档：' + (reader().entry()?.title || reader().source()) }),
      });
      toggle.addEventListener('click', () => comments.toggle());
      document.getElementById('edit-doc').addEventListener('click', openEditor);
      const wanted = Number(new URLSearchParams(location.search).get('comment'));
      if (wanted) { comments.focus(wanted); setTimeout(() => flash(wanted), 800); }
    }
    comments.refresh().then(() => highlight(comments.items));
  });

  function openEditor() {
    const entry = reader().entry();
    const markdown = /\.(md|markdown)$/i.test(reader().source());
    VinxEditor.open({
      target: target(),
      title: (entry?.title || reader().source()) + ' · ' + reader().source(),
      preview: markdown ? text => reader().vm.compiler.compile(text) : null,
      onClose: changed => { if (changed) location.reload(); },
    });
  }
})();
