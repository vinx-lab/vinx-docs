/* 工作簿只读预览：在浏览器里解析已发布的原件，不改写源文件，也不提供编辑。 */
(async function () {
  const label = document.getElementById('sheet-label');
  const tabs = document.getElementById('tabs');
  const body = document.getElementById('sheet');
  const download = document.getElementById('download');
  const base = location.pathname.replace(/sheet\.html$/, '');
  DocsifyXTheme.bind(document.getElementById('theme-toggle'));
  const projectLink = document.getElementById('project-link');
  projectLink.href = base;
  fetch(base + 'manifest.json', { cache: 'no-store' })
    .then(response => response.ok ? response.json() : null)
    .then(meta => { if (meta) projectLink.textContent = meta.name; })
    .catch(() => { /* 表格本身仍可预览，项目名拿不到就保持默认文字 */ });

  function fail(text) {
    body.textContent = text;
    body.classList.add('sheet-error');
  }

  const target = new URLSearchParams(location.search).get('f') || '';
  // 只允许打开本项目raw目录下的相对路径，不接受绝对路径或上跳。
  if (!target || target.startsWith('/') || target.split('/').includes('..')) {
    fail('缺少有效的表格路径。');
    return;
  }
  const rawUrl = base + 'raw/' + target.split('/').map(encodeURIComponent).join('/');
  label.textContent = target;
  document.title = target + ' · Vinx Docs';
  download.href = rawUrl;

  let workbook;
  try {
    const response = await fetch(rawUrl, { cache: 'no-store' });
    if (!response.ok) throw new Error('未找到该表格，可能已被排除或尚未同步。');
    workbook = XLSX.read(await response.arrayBuffer(), { type: 'array', cellDates: true, cellStyles: false });
  } catch (error) {
    fail(error.message || '表格解析失败，可以点右上角下载原件。');
    return;
  }

  function show(name) {
    const sheet = workbook.Sheets[name];
    body.classList.remove('sheet-error');
    body.innerHTML = XLSX.utils.sheet_to_html(sheet, { id: 'grid', editable: false });
    for (const button of tabs.children) {
      button.setAttribute('aria-selected', String(button.textContent === name));
    }
  }

  if (!workbook.SheetNames.length) { fail('这个工作簿没有任何工作表。'); return; }
  for (const name of workbook.SheetNames) {
    const button = document.createElement('button');
    button.type = 'button'; button.role = 'tab'; button.textContent = name;
    button.addEventListener('click', () => show(name));
    tabs.append(button);
  }
  show(workbook.SheetNames[0]);
})();
