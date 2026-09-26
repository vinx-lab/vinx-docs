/* 页面内编辑器：CodeMirror 5 源码编辑 + 可选实时预览，保存带基准版本；
   冲突时给出逐行对比，由人决定，绝不静默覆盖。阅读页和发布页共用。 */
(function (global) {
  const CM_FILES = ['codemirror.js', 'xml.js', 'javascript.js', 'css.js', 'htmlmixed.js', 'markdown.js',
    'continuelist.js', 'active-line.js', 'matchbrackets.js', 'placeholder.js'];
  let cmReady = null;

  function loadScript(src) {
    return new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = src; script.onload = resolve;
      script.onerror = () => reject(new Error('编辑器组件加载失败：' + src));
      document.head.append(script);
    });
  }
  function loadCodeMirror() {
    if (!cmReady) {
      const css = document.createElement('link');
      css.rel = 'stylesheet'; css.href = '/vendor/codemirror/codemirror.css';
      document.head.append(css);
      // 核心必须先加载，语法模式依赖它；其余按顺序加载即可。
      cmReady = CM_FILES.reduce((chain, name) => chain.then(() => loadScript('/vendor/codemirror/' + name)), Promise.resolve());
    }
    return cmReady;
  }

  async function api(path, body) {
    const options = body === undefined ? { cache: 'no-store', credentials: 'omit' }
      : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), cache: 'no-store', credentials: 'omit' };
    let response;
    try { response = await fetch(path, options); } catch (_) { throw new Error('连不上 Vinx Docs 服务，内容还在编辑器里，稍后再保存。'); }
    let data = {};
    try { data = await response.json(); } catch (_) { /* 非JSON错误页 */ }
    if (response.status === 409) { const error = new Error(data.error || '文件已被修改'); error.conflict = data; throw error; }
    if (!response.ok) throw new Error(data.error || ('服务返回 ' + response.status));
    return data;
  }

  // 逐行对比（LCS）。文档一般不长；超过上限退化为整块对比，避免卡住页面。
  function diffLines(a, b) {
    const x = a.split('\n'), y = b.split('\n');
    if (x.length * y.length > 4000000) return [...x.map(t => ['-', t]), ...y.map(t => ['+', t])];
    const n = x.length, m = y.length;
    const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--)
      dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    const out = [];
    let i = 0, j = 0;
    while (i < n && j < m) {
      if (x[i] === y[j]) { out.push([' ', x[i]]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) out.push(['-', x[i++]]);
      else out.push(['+', y[j++]]);
    }
    while (i < n) out.push(['-', x[i++]]);
    while (j < m) out.push(['+', y[j++]]);
    return out;
  }
  function renderDiff(a, b, labels) {
    const box = document.createElement('div'); box.className = 'vinx-diff';
    const legend = document.createElement('p'); legend.className = 'vinx-diff-legend';
    legend.innerHTML = '<span class="del">－ ' + labels[0] + '</span><span class="add">＋ ' + labels[1] + '</span>';
    const pre = document.createElement('pre');
    const rows = diffLines(a, b);
    // 大段相同内容折叠，只保留改动前后各2行。
    const keep = new Set();
    rows.forEach((row, index) => { if (row[0] !== ' ') for (let k = index - 2; k <= index + 2; k++) keep.add(k); });
    let skipped = 0;
    rows.forEach((row, index) => {
      if (!keep.has(index)) { skipped++; return; }
      if (skipped) { const gap = document.createElement('span'); gap.className = 'gap'; gap.textContent = `… 省略 ${skipped} 行相同内容 …\n`; pre.append(gap); skipped = 0; }
      const line = document.createElement('span');
      line.className = row[0] === '-' ? 'del' : row[0] === '+' ? 'add' : 'same';
      line.textContent = row[0] + ' ' + row[1] + '\n';
      pre.append(line);
    });
    if (!rows.some(row => row[0] !== ' ')) pre.textContent = '两边内容相同。';
    box.append(legend, pre);
    return box;
  }

  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function button(text, cls) { const b = el('button', cls || 'ghost-button', text); b.type = 'button'; return b; }
  const when = seconds => new Date(seconds * 1000).toLocaleString();

  /* options: { target, title, preview(text)->html 或 null, dock（停靠右侧，不遮住页面）, onSaved(result), onClose(changed) } */
  async function open(options) {
    document.querySelector('.vinx-editor')?.remove();
    const root = el('div', 'vinx-editor'); root.setAttribute('role', 'dialog'); root.setAttribute('aria-label', '编辑源文件');
    const bar = el('div', 'vinx-editor-bar');
    const title = el('strong', 'vinx-editor-title', options.title || options.target);
    const state = el('span', 'vinx-editor-state', '正在读取…');
    const tabs = el('div', 'vinx-editor-tabs');
    const tabEdit = button('编辑'), tabPreview = button('预览');
    if (options.preview) tabs.append(tabEdit, tabPreview);
    const historyButton = button('历史'), saveButton = button('保存', 'primary-button'), closeButton = button('关闭');
    bar.append(title, state, tabs, historyButton, saveButton, closeButton);
    const body = el('div', 'vinx-editor-body' + (options.preview ? ' with-preview' : ''));
    const editorPane = el('div', 'vinx-editor-pane');
    const previewPane = el('div', 'vinx-editor-preview markdown-section');
    body.append(editorPane);
    if (options.preview) body.append(previewPane);
    const side = el('aside', 'vinx-editor-side'); side.hidden = true;
    root.append(bar, body, side);
    if (options.dock) root.classList.add('docked');
    document.body.append(root);
    document.body.classList.add(options.dock ? 'vinx-editing-docked' : 'vinx-editing');

    let doc, cm, changedOnDisk = false, previewTimer = 0, draftTimer = 0;
    const draftKey = 'vinx-draft:' + options.target;
    const setState = (text, kind) => { state.textContent = text; state.dataset.state = kind || ''; };
    const dirty = () => cm && cm.getValue() !== doc.content;

    function close() {
      if (dirty() && !confirm('有未保存的修改，确定关闭？（草稿会保留在本标签页里）')) return;
      root.remove(); document.body.classList.remove('vinx-editing', 'vinx-editing-docked');
      document.removeEventListener('keydown', onKey, true);
      options.onClose && options.onClose(changedOnDisk);
    }
    function onKey(event) {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') { event.preventDefault(); save(); }
      else if (event.key === 'Escape' && side.hidden) { event.preventDefault(); close(); }
    }
    document.addEventListener('keydown', onKey, true);
    closeButton.addEventListener('click', close);
    tabEdit.addEventListener('click', () => { body.dataset.view = 'edit'; cm && cm.refresh(); });
    tabPreview.addEventListener('click', () => { body.dataset.view = 'preview'; renderPreview(); });

    function renderPreview() {
      if (!options.preview || !cm) return;
      try { previewPane.innerHTML = options.preview(cm.getValue()); }
      catch (error) { previewPane.textContent = '预览失败：' + error.message; }
    }

    function showSide(heading, content, actions) {
      side.replaceChildren();
      const head = el('div', 'vinx-side-head');
      const back = button('返回编辑');
      back.addEventListener('click', () => { side.hidden = true; cm.focus(); });
      head.append(el('strong', '', heading), back);
      const foot = el('div', 'vinx-side-actions');
      actions.forEach(action => foot.append(action));
      side.append(head, content, foot);
      side.hidden = false;
    }

    function conflict(data) {
      const mine = cm.getValue();
      const note = el('p', 'vinx-side-note', '你打开编辑器之后，这个文件被别人（通常是 agent）改过了。下面是「现在磁盘上的版本」和「你的版本」的差别：');
      const keepMine = button('用我的版本覆盖', 'danger-button');
      const takeTheirs = button('放弃我的修改，载入最新版');
      const copyMine = button('复制我的版本，再载入最新版');
      keepMine.addEventListener('click', () => { doc.version = data.version; side.hidden = true; save(); });
      takeTheirs.addEventListener('click', () => { if (!confirm('放弃你的修改？')) return; load(data.content, data.version); side.hidden = true; setState('已载入最新版', 'ok'); });
      copyMine.addEventListener('click', async () => {
        try { await navigator.clipboard.writeText(mine); } catch (_) {
          const area = el('textarea'); area.value = mine; document.body.append(area); area.select(); document.execCommand('copy'); area.remove();
        }
        load(data.content, data.version); side.hidden = true; setState('你的版本已复制到剪贴板，编辑器里是最新版', 'ok');
      });
      const wrap = el('div'); wrap.append(note, renderDiff(data.content, mine, ['磁盘上的最新版', '你的版本']));
      showSide('保存冲突', wrap, [keepMine, takeTheirs, copyMine]);
      setState('保存冲突，需要你决定', 'error');
    }

    async function showHistory() {
      let items;
      try { items = (await api('/api/file/history?target=' + encodeURIComponent(options.target))).history; }
      catch (error) { setState(error.message, 'error'); return; }
      const list = el('div', 'vinx-history');
      if (!items.length) list.append(el('p', 'vinx-side-note', '还没有历史版本。每次在页面上保存前，旧内容会自动留一份（最多20份）。'));
      for (const item of items) {
        const row = button(when(item.savedAt) + ' · ' + item.size + ' 字节', 'vinx-history-row');
        row.addEventListener('click', async () => {
          const old = (await api('/api/file/history/item?target=' + encodeURIComponent(options.target) + '&id=' + item.id)).content;
          const restore = button('把这个版本放进编辑器', 'primary-button');
          restore.addEventListener('click', () => { cm.setValue(old); side.hidden = true; setState('已放入历史版本，确认后点保存', 'warn'); });
          const back = button('返回列表'); back.addEventListener('click', showHistory);
          const wrap = el('div'); wrap.append(el('p', 'vinx-side-note', when(item.savedAt) + ' 保存前的内容，与编辑器当前内容对比：'), renderDiff(old, cm.getValue(), ['历史版本', '编辑器当前']));
          showSide('历史版本', wrap, [restore, back]);
        });
        list.append(row);
      }
      showSide('历史版本', list, []);
    }
    historyButton.addEventListener('click', showHistory);

    function load(content, version) {
      doc.content = content; doc.version = version;
      cm.setValue(content); cm.clearHistory();
      try { sessionStorage.removeItem(draftKey); } catch (_) { /* 忽略 */ }
      renderPreview();
    }

    async function save() {
      if (!cm || saveButton.disabled) return;
      if (!dirty()) { setState('没有修改', 'ok'); return; }
      saveButton.disabled = true; setState('正在保存…');
      const content = cm.getValue();
      try {
        const result = await api('/api/file/save', { target: options.target, content, baseVersion: doc.version });
        doc.content = content; doc.version = result.version; changedOnDisk = true;
        try { sessionStorage.removeItem(draftKey); } catch (_) { /* 忽略 */ }
        setState('已保存 ' + new Date().toLocaleTimeString() + (result.kind === 'doc' && !result.refreshed ? '（阅读副本稍后同步）' : ''), 'ok');
        options.onSaved && options.onSaved(result);
      } catch (error) {
        if (error.conflict) conflict(error.conflict); else setState(error.message, 'error');
      } finally { saveButton.disabled = false; }
    }
    saveButton.addEventListener('click', save);

    try {
      const [data] = await Promise.all([api('/api/file?target=' + encodeURIComponent(options.target)), loadCodeMirror()]);
      doc = data;
      title.textContent = options.title || data.title || data.path;
      title.title = data.source || '';
      cm = CodeMirror(editorPane, {
        value: data.content, mode: data.mode === 'markdown' ? { name: 'markdown', highlightFormatting: true } : data.mode,
        lineNumbers: true, lineWrapping: true, styleActiveLine: true, matchBrackets: true, indentUnit: 2, tabSize: 2,
        extraKeys: { Enter: data.mode === 'markdown' ? 'newlineAndIndentContinueMarkdownList' : 'newlineAndIndent', Tab: cm => cm.execCommand(cm.somethingSelected() ? 'indentMore' : 'insertSoftTab') },
      });
      let draft = null;
      try { draft = JSON.parse(sessionStorage.getItem(draftKey) || 'null'); } catch (_) { draft = null; }
      if (draft && draft.version === data.version && draft.content !== data.content) {
        cm.setValue(draft.content); setState('已恢复本标签页里未保存的草稿', 'warn');
      } else setState(data.crlf ? '已载入（保存时保持 CRLF 换行）' : '已载入');
      cm.on('change', () => {
        clearTimeout(previewTimer); previewTimer = setTimeout(renderPreview, 250);
        clearTimeout(draftTimer); draftTimer = setTimeout(() => {
          try { sessionStorage.setItem(draftKey, JSON.stringify({ version: doc.version, content: cm.getValue() })); } catch (_) { /* 忽略 */ }
        }, 500);
        if (state.dataset.state !== 'error') setState('有未保存的修改', 'warn');
      });
      renderPreview();
      body.dataset.view = 'edit';
      cm.focus();
      if (options.line) cm.setCursor({ line: options.line - 1, ch: 0 });
    } catch (error) {
      setState(error.message, 'error');
      saveButton.disabled = true; historyButton.disabled = true;
    }
    return { close };
  }

  global.VinxEditor = { open, api, renderDiff };
})(window);
