// 侧栏目录折叠：目录默认折叠，当前页面所在目录自动展开。
// 可折叠的有两层：顶层（文档目录分组，或只有一个文档目录时的各个目录）和分组下的子目录。
// Docsify 每次切页都会重画侧栏，所以每次重画后重新套一遍；手动开合记在本标签页（sessionStorage），按项目区分。
(function () {
  const KEY = 'docsify-x-fold:' + location.pathname;
  let state = {};
  try { state = JSON.parse(sessionStorage.getItem(KEY) || '{}') || {}; } catch (_) { state = {}; }
  function save() {
    try { sessionStorage.setItem(KEY, JSON.stringify(state)); } catch (_) { /* 隐私模式忽略 */ }
  }

  function setOpen(li, open) {
    li.classList.toggle('folded', !open);
    const button = li.querySelector(':scope > .sidebar-dir-toggle');
    if (button) button.setAttribute('aria-expanded', String(open));
  }

  function toolButton(label, open, dirs) {
    const button = document.createElement('button');
    button.type = 'button'; button.textContent = label;
    button.addEventListener('click', event => {
      event.stopPropagation(); // 手机宽度下 Docsify 点正文任意处会收起侧栏
      dirs.forEach(li => { state[li.dataset.foldKey] = open; setOpen(li, open); });
      save();
    });
    return button;
  }

  // 当前页面对应的侧栏链接。Docsify 自带的 active 按去掉 .md 的地址前缀匹配，
  // 和这里带 .md 的路由对不上，所以自己按 hash 路径比较。
  const routeKey = value => {
    try { value = decodeURIComponent(value); } catch (_) { /* 保持原样 */ }
    return value.replace(/\.md$/, '');
  };
  function currentLink(list) {
    let path = location.hash.replace(/^#/, '').split('?')[0];
    if (!path || path === '/') {
      const reader = window.DocsifyXReader;
      path = '/' + (reader && reader.meta ? reader.meta.home : '');
    }
    const key = routeKey(path);
    // 取最后一个匹配：顶部「本项目首页」和目录里的同一文件都匹配时，以目录里那条为准。
    const matches = Array.from(list.querySelectorAll('li a[href^="#/"]'))
      .filter(a => routeKey(a.getAttribute('href').slice(1).split('?')[0]) === key);
    return matches.length ? matches[matches.length - 1] : null;
  }

  // 目录行是 _sidebar.md 里的 "- 目录"：自身没有链接，下面挂一个列表。
  const dirList = li => (li.querySelector(':scope > a') ? null : li.querySelector(':scope > ul'));

  // 把一个目录行换成开合按钮。记忆的键按层级区分：顶层是 "top:名字"，子目录是 "sub:分组\n名字"，
  // 分组名和别的分组下的同名子目录不会互相影响。
  function makeDir(li, sub, key, current) {
    const label = Array.from(li.childNodes).filter(node => node !== sub).map(node => node.textContent).join('').trim();
    Array.from(li.childNodes).forEach(node => { if (node !== sub) node.remove(); });
    const button = document.createElement('button');
    button.type = 'button'; button.className = 'sidebar-dir-toggle'; button.textContent = label; button.title = label;
    const foldKey = key(label);
    button.addEventListener('click', event => {
      event.stopPropagation();
      const open = li.classList.contains('folded');
      state[foldKey] = open; save(); setOpen(li, open);
    });
    li.prepend(button);
    li.classList.add('sidebar-dir'); li.dataset.dir = label; li.dataset.foldKey = foldKey;
    const here = !!current && sub.contains(current);
    li.classList.toggle('has-active', here);
    setOpen(li, here || state[foldKey] === true);
    return label;
  }

  function enhance(list) {
    list.dataset.fold = '1';
    const current = currentLink(list);
    if (current) current.parentElement.classList.add('sidebar-current');
    const dirs = [];
    list.querySelectorAll(':scope > li').forEach(li => {
      const sub = dirList(li);
      if (!sub) return;
      const group = makeDir(li, sub, label => 'top:' + label, current);
      dirs.push(li);
      sub.querySelectorAll(':scope > li').forEach(child => {
        const inner = dirList(child);
        if (!inner) return;
        makeDir(child, inner, label => 'sub:' + group + '\n' + label, current);
        child.classList.add('sidebar-subdir');
        li.classList.add('sidebar-group');
        dirs.push(child);
      });
    });
    if (dirs.length < 2) return;
    const tools = document.createElement('li');
    tools.className = 'sidebar-fold-tools';
    tools.append(toolButton('全部展开', true, dirs), toolButton('全部折叠', false, dirs));
    list.querySelector(':scope > li.sidebar-dir').before(tools);
  }

  function scan() {
    const list = document.querySelector('.sidebar-nav > ul:not([data-fold])');
    if (list) enhance(list);
  }
  new MutationObserver(scan).observe(document.documentElement, { childList: true, subtree: true });
  scan();
})();
