/* 界面小图标：内联 SVG，描边跟随文字颜色，不依赖图标字体或外部资源。
   图形是自绘的简单几何路径。旁边有文字时是装饰图标，读屏跳过。 */
(function () {
  const PATHS = {
    warn: 'M12 3.5 2.5 20h19L12 3.5Zm0 6v5m0 3v.01',
    error: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18Zm-3.5 5.5 7 7m0-7-7 7',
    comment: 'M4 5h16v11H9l-4 4v-4H4V5Z',
    external: 'M14 4h6v6m0-6-9 9M18 14v6H4V6h6',
    plus: 'M12 5v14M5 12h14',
    sync: 'M20 11a8 8 0 0 0-14.5-4.5L4 8m0-4v4h4m-4 5a8 8 0 0 0 14.5 4.5L20 16m0 4v-4h-4',
    close: 'M6 6l12 12M18 6 6 18',
  };
  window.VinxIcon = function (name, label) {
    const ns = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('class', 'icon');
    if (label) { svg.setAttribute('role', 'img'); svg.setAttribute('aria-label', label); }
    else svg.setAttribute('aria-hidden', 'true');
    const path = document.createElementNS(ns, 'path');
    path.setAttribute('d', PATHS[name] || '');
    svg.append(path);
    return svg;
  };
  // 静态页面里写 <span data-icon="plus"></span>，加载后替换成图标。
  const fill = () => document.querySelectorAll('[data-icon]').forEach(node => node.replaceWith(window.VinxIcon(node.dataset.icon)));
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', fill); else fill();
})();
