/* 「下载源文件」下拉菜单：按标题命名（默认）或按原文件名。阅读页和页面列表共用。
   页面里放一个 <details class="download-menu">，用 VinxDownloadMenu.set(元素, 下载地址, {title, file}) 填入。 */
(function () {
  function set(menu, url, names) {
    if (!menu) return;
    if (!url || !names) { menu.hidden = true; menu.open = false; return; }
    const [byTitle, byFile] = menu.querySelectorAll('.download-options a');
    byTitle.href = url; byTitle.querySelector('small').textContent = names.title;
    byFile.href = url + '?name=file'; byFile.querySelector('small').textContent = names.file;
    // 两个名字一样时只留一项。
    byFile.hidden = names.file === names.title;
    menu.hidden = false;
  }
  function bind(menu) {
    if (!menu || menu.dataset.bound) return;
    menu.dataset.bound = '1';
    menu.querySelectorAll('.download-options a').forEach(link => link.addEventListener('click', () => { menu.open = false; }));
    document.addEventListener('click', event => { if (menu.open && !menu.contains(event.target)) menu.open = false; });
    menu.addEventListener('keydown', event => { if (event.key === 'Escape' && menu.open) { menu.open = false; menu.querySelector('summary').focus(); } });
  }
  window.VinxDownloadMenu = { set: (menu, url, names) => { bind(menu); set(menu, url, names); } };
})();
