(async function () {
  const status = document.getElementById('admin-status');
  const stats = document.getElementById('runtime-stats');
  const split = document.getElementById('admin-split');
  const nav = document.getElementById('admin-nav');
  const navEmpty = document.getElementById('admin-nav-empty');
  const detail = document.getElementById('admin-detail');
  const filter = document.getElementById('project-filter');
  const empty = document.getElementById('admin-empty');
  const projectCount = document.getElementById('project-count');
  const settingsForm = document.getElementById('settings-form');
  const addForm = document.getElementById('add-form');
  const newPath = document.getElementById('new-path');
  const refreshAll = document.getElementById('refresh-all');
  const toggleAdd = document.getElementById('toggle-add');
  const addPanel = document.getElementById('add-panel');
  const fields = {
    autoSync: document.getElementById('auto-sync'),
    debounceSeconds: document.getElementById('debounce'),
    recentDays: document.getElementById('recent-days'),
    recentLimit: document.getElementById('recent-limit'),
    displayName: document.getElementById('display-name'),
  };
  let config = null;
  let selectedId = null;
  let draft = null;
  // 保存结果显示在项目详情的按钮下方；详情会在保存后重绘，所以按项目记住最近一条。
  let detailNote = null;

  const relative = seconds => {
    if (!seconds) return '未知';
    const gap = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
    if (gap < 90) return '刚刚';
    if (gap < 3600) return Math.floor(gap / 60) + ' 分钟前';
    if (gap < 86400) return Math.floor(gap / 3600) + ' 小时前';
    return Math.floor(gap / 86400) + ' 天前';
  };

  function message(text, state = '') {
    status.textContent = text;
    status.dataset.state = state;
  }

  async function call(endpoint, body, method = 'POST') {
    const options = { method, cache: 'no-store', credentials: 'omit' };
    if (method === 'POST') {
      options.headers = { 'Content-Type': 'application/json' };
      options.body = JSON.stringify(body || {});
    }
    let response;
    try {
      response = await fetch(endpoint, options);
    } catch (_) {
      throw new Error('无法连接管理接口；请确认服务仍在运行。');
    }
    if (!response.headers.get('content-type')?.includes('application/json')) {
      throw new Error('管理服务暂不可用，请稍后重试。');
    }
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || '操作失败，请重试。');
    return result;
  }

  const el = (tag, cls, text) => { const node = document.createElement(tag); if (cls) node.className = cls; if (text !== undefined) node.textContent = text; return node; };
  const withIcon = (node, name) => { node.prepend(VinxIcon(name)); return node; };

  // 运行状态压成一行：每项「标签 值」，出错时整条标红并单独列出错误。
  function renderStats(info) {
    const mode = info.autoSync?.mode || 'off';
    const rows = [
      ['自动同步', mode === 'off' ? '已关闭' : mode.startsWith('inotify') ? `文件事件监听 · ${mode.replace(/\D+/g, '')} 个目录` : '定时轮询'],
      ['已收录', (info.lastBuild?.projects || []).reduce((sum, item) => sum + item.fileCount, 0) + ' 个文件'],
      ['上次构建', info.lastBuild?.completedAt ? relative(info.lastBuild.completedAt) : '未知'],
      ['上次自动同步', info.autoSync?.lastRun ? relative(info.autoSync.lastRun) : '尚未触发'],
      ['监听', (info.server?.host || '?') + ':' + (info.server?.port ?? '?')],
    ];
    stats.replaceChildren();
    const error = info.autoSync?.lastError;
    stats.dataset.state = error ? 'error' : 'ok';
    const head = el('span', 'status-live', '运行中');
    head.title = '管理接口 PID ' + info.pid;
    stats.append(head);
    for (const [label, value] of rows) {
      const item = el('span', 'status-item');
      item.append(el('span', 'status-label', label), el('span', 'status-value', value));
      stats.append(item);
    }
    if (error) stats.append(withIcon(el('span', 'status-error', '自动同步出错：' + error), 'error'));
  }

  function detailBlock(summaryText, items, emptyText) {
    const details = document.createElement('details');
    const summary = document.createElement('summary');
    summary.textContent = `${summaryText}（${items.length}）`;
    details.append(summary);
    const body = document.createElement('div');
    body.className = 'detail-body';
    if (!items.length) {
      body.textContent = emptyText;
    } else {
      const table = document.createElement('table');
      for (const item of items) {
        const at = item.lastIndexOf(':');
        const row = table.insertRow();
        const path = row.insertCell(); path.textContent = item.slice(0, at);
        const reason = row.insertCell(); reason.textContent = item.slice(at + 1); reason.className = 'reason';
      }
      body.append(table);
    }
    details.append(body);
    return details;
  }

  const unusedHint = '规则匹配的是相对文档根的完整路径，按扩展名排除要写成 *.py 而不是 .py；如果这些条目本来就被强制安全规则挡住，也会出现这个提示。';

  function projectMatches(project, keyword) {
    if (!keyword) return true;
    const haystack = [project.name, project.id, ...project.roots.map(item => item.path)].join('\n').toLowerCase();
    return haystack.includes(keyword);
  }

  function isDirty() {
    return Boolean(draft && draft.read() !== draft.initial);
  }

  // 列表切换、同步全部、接入新项目都会重绘详情区；有未保存的改动时先确认，避免悄悄丢掉。
  function confirmDiscard() {
    return !isDirty() || confirm('当前项目有未保存的改动（名称、文档目录或排除规则），继续会丢弃这些改动。确定继续？');
  }

  function renderProjects() {
    const projects = config.projects;
    projectCount.textContent = projects.length ? `（${projects.length}）` : '';
    if (!projects.some(item => item.id === selectedId)) selectedId = projects[0]?.id || null;
    split.hidden = !projects.length;
    empty.hidden = Boolean(projects.length);
    empty.textContent = '尚无已接入项目，点右上方「接入项目」填写文档目录路径。';
    renderNav();
    renderDetail();
  }

  function renderNav() {
    const keyword = filter.value.trim().toLowerCase();
    nav.replaceChildren();
    let shown = 0;
    for (const project of config.projects) {
      if (!projectMatches(project, keyword)) continue;
      shown += 1;
      const item = document.createElement('li');
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'admin-nav-item';
      if (project.id === selectedId) button.setAttribute('aria-current', 'true');
      const name = document.createElement('strong'); name.textContent = project.name;
      const meta = document.createElement('span');
      meta.className = 'admin-nav-meta';
      meta.textContent = `${project.fileCount} 个文件 · ${project.exclude.length} 条排除 · ${relative(project.updatedAt)}`;
      button.append(name, meta);
      if (project.error) {
        const badge = document.createElement('span');
        badge.className = 'admin-nav-warn admin-nav-error';
        badge.textContent = '目录失效';
        withIcon(badge, 'error');
        badge.title = project.error;
        button.append(badge);
      } else if (project.warnings.length) {
        const badge = document.createElement('span');
        badge.className = 'admin-nav-warn';
        badge.textContent = String(project.warnings.length);
        withIcon(badge, 'warn');
        badge.title = `${project.warnings.length} 个已收录文件疑似含明文口令或密钥`;
        button.append(badge);
      }
      button.addEventListener('click', () => {
        if (project.id !== selectedId && !confirmDiscard()) return;
        if (project.id !== selectedId) detailNote = null;
        selectedId = project.id;
        split.dataset.view = 'detail';
        renderNav();
        renderDetail();
        detail.focus({ preventScroll: true });
        if (window.matchMedia('(max-width: 768px)').matches) split.scrollIntoView({ block: 'start' });
      });
      item.append(button);
      nav.append(item);
    }
    navEmpty.hidden = shown > 0;
  }

  function renderDetail() {
    detail.replaceChildren();
    draft = null;
    const project = config.projects.find(item => item.id === selectedId);
    if (!project) return;

    const back = document.createElement('button');
    back.type = 'button'; back.className = 'ghost-button admin-back'; back.textContent = '← 返回项目列表';
    back.addEventListener('click', () => { split.dataset.view = 'list'; });

    const head = document.createElement('div');
    head.className = 'admin-detail-head';
    const nameInput = document.createElement('input');
    nameInput.type = 'text'; nameInput.value = project.name; nameInput.className = 'name-input';
    nameInput.setAttribute('aria-label', '项目显示名');
    const open = document.createElement('a');
    open.className = 'ghost-button'; open.href = project.url; open.textContent = '打开';
    head.append(nameInput, open);

    const meta = document.createElement('p');
    meta.className = 'card-note';
    meta.textContent = `${project.id} · ${project.roots.length} 个文档目录 · 已收录 ${project.fileCount} 个文件 · 最近更新 ${relative(project.updatedAt)}`;

    const rootsLabel = document.createElement('label');
    rootsLabel.className = 'field-block';
    rootsLabel.textContent = '文档目录（每行一条，写成「项目内位置 = 主机上的绝对路径」；项目内位置留空表示直接挂在项目根下）';
    const rootsInput = document.createElement('textarea');
    rootsInput.rows = Math.min(10, Math.max(2, project.roots.length + 1));
    rootsInput.value = project.roots.map(item => (item.prefix ? item.prefix + ' = ' : '= ') + item.path).join('\n');
    rootsInput.spellcheck = false;
    rootsLabel.append(rootsInput);

    const label = document.createElement('label');
    label.className = 'field-block';
    label.textContent = '排除规则（每行一条，匹配相对文档根的路径，支持 * 和 **/ 通配；按扩展名排除写 *.py）';
    const excludes = document.createElement('textarea');
    excludes.rows = Math.min(10, Math.max(3, project.exclude.length + 1));
    excludes.value = project.exclude.join('\n');
    excludes.spellcheck = false;
    label.append(excludes);

    // 没起作用的规则逐条列成标签，点 × 直接从上面的排除规则里删掉（仍需保存）。
    const unused = project.unusedExclude || [];
    const unusedNote = el('div', 'unused-exclude');
    unusedNote.hidden = !unused.length;
    unusedNote.append(withIcon(el('p', 'card-warning', `${unused.length} 条规则没有额外排除任何条目：`), 'warn'));
    const chips = el('ul', 'rule-chips');
    for (const rule of unused) {
      const chip = el('li', 'rule-chip');
      const drop = el('button', 'rule-chip-remove');
      drop.type = 'button';
      drop.setAttribute('aria-label', '从排除规则中删除 ' + rule);
      drop.title = '从排除规则中删除（保存后生效）';
      drop.append(VinxIcon('close'));
      drop.addEventListener('click', () => {
        excludes.value = excludes.value.split('\n').filter(line => line.trim() !== rule).join('\n');
        chip.remove();
      });
      chip.append(el('code', '', rule), drop);
      chips.append(chip);
    }
    unusedNote.append(chips, el('p', 'card-note', unusedHint));

    const actions = document.createElement('div');
    actions.className = 'card-actions';
    const save = document.createElement('button'); save.type = 'button'; save.textContent = '保存并同步';
    actions.append(save);
    // 破坏性操作单独放在详情最底部，和保存隔开。
    const danger = el('div', 'danger-zone');
    const dangerText = el('div');
    dangerText.append(el('strong', '', '取消接入'), el('p', 'card-note', '移除这个项目并删除 Vinx Docs 生成的阅读副本，原文目录不会被改动。'));
    const remove = el('button', 'danger', '取消接入');
    remove.type = 'button';
    danger.append(dangerText, remove);
    const busy = value => { save.disabled = remove.disabled = value; };

    const note = document.createElement('p');
    note.className = 'manage-status card-status';
    note.setAttribute('role', 'status');
    note.setAttribute('aria-live', 'polite');
    const noteMessage = (text, state = '') => {
      detailNote = { id: project.id, text, state };
      note.textContent = text;
      note.dataset.state = state;
      note.hidden = false;
    };
    note.hidden = detailNote?.id !== project.id;
    if (!note.hidden) { note.textContent = detailNote.text; note.dataset.state = detailNote.state; }

    detail.append(back, head, meta);
    if (project.error) {
      const broken = document.createElement('p');
      broken.className = 'card-warning card-error';
      broken.textContent = `文档目录不可用：${project.error}。目录可能已被改名或删除；在下方改成新的路径后保存，或在底部取消接入。修好之前，同步全部会失败。`;
      withIcon(broken, 'error');
      detail.append(broken);
    }
    if (project.warnings.length) {
      const warn = document.createElement('p');
      warn.className = 'card-warning';
      warn.textContent = `${project.warnings.length} 个已收录文件疑似含明文口令或密钥。确认后可以把它们加进下面的排除规则。`;
      withIcon(warn, 'warn');
      const add = document.createElement('button');
      add.type = 'button'; add.className = 'ghost-button'; add.textContent = '全部加入排除规则';
      add.addEventListener('click', () => {
        const paths = project.warnings.map(item => item.slice(0, item.lastIndexOf(':')));
        const current = excludes.value.split('\n').map(line => line.trim()).filter(Boolean);
        excludes.value = [...new Set([...current, ...paths])].join('\n');
        excludes.rows = Math.min(14, excludes.value.split('\n').length + 1);
      });
      warn.append(' ', add);
      detail.append(warn);
    }

    detail.append(rootsLabel, label, unusedNote, actions, note);
    detail.append(detailBlock('疑似含明文口令的已收录文件', project.warnings, '没有命中。'));
    detail.append(detailBlock('被排除的条目', project.skipped, '没有条目被排除。'));
    detail.append(danger);

    const read = () => JSON.stringify([nameInput.value, rootsInput.value, excludes.value]);
    draft = { read, initial: read() };

    save.addEventListener('click', async () => {
      busy(true);
      noteMessage('正在保存并同步…');
      try {
        const roots = rootsInput.value.split('\n').map(line => line.trim()).filter(Boolean).map(line => {
          const at = line.indexOf('=');
          if (at < 0) return { prefix: '', path: line };
          return { prefix: line.slice(0, at).trim(), path: line.slice(at + 1).trim() };
        });
        if (!roots.length) throw new Error('至少要保留一个文档目录。');
        const result = await call('/api/project/update', {
          id: project.id,
          name: nameInput.value,
          roots,
          exclude: excludes.value.split('\n').map(line => line.trim()).filter(Boolean),
        });
        const saved = `已保存：${result.fileCount} 个文件，排除 ${result.skippedCount} 个条目，复用 ${result.reusedCount} 个未变更文件。`;
        const stale = result.projects.find(item => item.id === project.id)?.unusedExclude || [];
        if (stale.length) noteMessage(`${saved}\n但这些规则没有额外排除任何条目：${stale.join('、')}。${unusedHint}`, 'warning');
        else noteMessage(saved, 'success');
        draft = null;
        await load();
      } catch (error) {
        noteMessage(error.message, 'error');
        busy(false);
      }
    });

    remove.addEventListener('click', async () => {
      const paths = project.roots.map(item => item.path).join('\n');
      if (!confirm(`取消接入「${project.name}」？\n\n只删除 Vinx Docs 生成的阅读副本，下列目录里的原文不会被改动：\n${paths}`)) return;
      busy(true);
      message('正在取消接入…');
      try {
        await call('/api/project/remove', { id: project.id });
        message('已取消接入，原项目文档未被改动。', 'success');
        draft = null;
        split.dataset.view = 'list';
        await load();
      } catch (error) {
        message(error.message, 'error');
        busy(false);
      }
    });
  }

  async function load() {
    config = await call('/api/config', null, 'GET');
    for (const [key, field] of Object.entries(fields)) {
      if (field.type === 'checkbox') field.checked = Boolean(config.settings[key]);
      else field.value = config.settings[key];
    }
    renderProjects();
    const info = await call('/api/status', null, 'GET');
    if (info.version) document.getElementById('app-version').textContent = 'v' + info.version;
    renderStats(info);
  }

  settingsForm.addEventListener('submit', async event => {
    event.preventDefault();
    message('正在保存设置…');
    try {
      const body = {
        autoSync: fields.autoSync.checked,
        debounceSeconds: Number(fields.debounceSeconds.value),
        recentDays: Number(fields.recentDays.value),
        recentLimit: Number(fields.recentLimit.value),
        displayName: fields.displayName.value.trim(),
      };
      const result = await call('/api/settings', body);
      message(result.settings.autoSync ? '设置已保存，自动同步已开启。' : '设置已保存，自动同步已关闭。', 'success');
      if (!isDirty()) await load();
    } catch (error) {
      message(error.message, 'error');
    }
  });

  addForm.addEventListener('submit', async event => {
    event.preventDefault();
    if (!confirmDiscard()) return;
    message('正在接入并同步…');
    try {
      const known = new Set(config?.projects.map(item => item.id));
      const result = await call('/api/projects', { docsPath: newPath.value.trim() });
      message(`已接入：共 ${result.fileCount} 个文件，排除 ${result.skippedCount} 个条目。`, result.warningCount ? 'warning' : 'success');
      newPath.value = '';
      addPanel.hidden = true;
      toggleAdd.setAttribute('aria-expanded', 'false');
      selectedId = result.projects.find(item => !known.has(item.id))?.id || selectedId;
      filter.value = '';
      split.dataset.view = 'detail';
      await load();
    } catch (error) {
      message(error.message, 'error');
    }
  });

  refreshAll.addEventListener('click', async () => {
    if (!confirmDiscard()) return;
    refreshAll.disabled = true;
    message('正在同步全部项目…');
    try {
      const result = await call('/api/refresh', {});
      message(`同步完成：${result.fileCount} 个文件，复用 ${result.reusedCount} 个未变更文件。`, 'success');
      await load();
    } catch (error) {
      message(error.message, 'error');
    } finally {
      refreshAll.disabled = false;
    }
  });

  filter.addEventListener('input', renderNav);
  toggleAdd.addEventListener('click', () => {
    const open = addPanel.hidden;
    addPanel.hidden = !open;
    toggleAdd.setAttribute('aria-expanded', String(open));
    if (open) newPath.focus();
  });
  window.addEventListener('beforeunload', event => {
    if (isDirty()) event.preventDefault();
  });

  DocsifyXTheme.bind(document.getElementById('theme-toggle'));

  try {
    await load();
    // 连接状态由顶部状态条表达，这里只留给操作结果。
    message('');
  } catch (error) {
    message(error.message + ' 请在主机执行 vinx-docs start 后重新加载页面。', 'error');
    empty.hidden = false;
    empty.textContent = '配置不可读。';
  }
})();
