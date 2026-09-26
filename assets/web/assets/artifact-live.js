/* 短链接页面里注入的脚本（控制服务在每个HTML末尾插入，带当时的版本号）：
   1. 自动刷新：清单里任一文件变化，版本号就变，页面重新加载。
   2. 批注：在发布页（外层）里打开时，响应外层的点选/框选请求，把定位信息回传；在页面上画批注编号。
   页面在沙箱里运行，origin 是 null；只和直接父窗口通信，传的都是定位信息，不含敏感数据。 */
(function () {
  if (window.__docsifyXLive) return;
  window.__docsifyXLive = true;
  const script = document.currentScript;
  const match = location.pathname.match(/^\/a\/([a-z0-9]+)\/(.*)$/);
  if (!match || !script) return;
  const code = match[1];
  let page = script.dataset.page || '';
  if (!page) { try { page = decodeURIComponent(match[2]); } catch (_) { page = match[2]; } }
  const url = '/a/' + code + '/__docsify_x/version';
  const loaded = script.dataset.version;
  let timer = 0;

  async function check() {
    if (document.hidden) return;
    try {
      const response = await fetch(url, { cache: 'no-store' });
      if (!response.ok) return;
      const latest = (await response.json()).version;
      if (latest && latest !== loaded) { clearInterval(timer); location.reload(); }
    } catch (_) { /* 服务重启或断网时下一轮再查 */ }
  }
  timer = setInterval(check, 2000);
  document.addEventListener('visibilitychange', check);

  const Z = 2147483000;
  const framed = window.parent !== window;

  function ready(fn) { if (document.body) fn(); else document.addEventListener('DOMContentLoaded', fn); }

  if (!framed) {
    // 单独打开时给一个回到发布页的入口，在那里才能批注和编辑。
    ready(() => {
      const link = document.createElement('a');
      link.href = '/published.html#' + code; link.textContent = '在 Vinx Docs 中批注';
      link.setAttribute('style', `position:fixed;right:12px;bottom:12px;z-index:${Z};font:12px/1.4 sans-serif;padding:6px 10px;` +
        'border-radius:4px;background:rgba(23,107,121,.92);color:#fff;text-decoration:none;box-shadow:0 2px 8px rgba(0,0,0,.25);opacity:.75');
      link.addEventListener('mouseenter', () => { link.style.opacity = '1'; });
      link.addEventListener('mouseleave', () => { link.style.opacity = '.75'; });
      document.body.append(link);
    });
    return;
  }

  const post = message => window.parent.postMessage(Object.assign({ vinx: true }, message), '*');

  function selectorFor(element) {
    const unique = id => { try { return document.querySelectorAll('#' + CSS.escape(id)).length === 1; } catch (_) { return false; } };
    if (element.id && unique(element.id)) return '#' + CSS.escape(element.id);
    const parts = [];
    let node = element;
    while (node && node.nodeType === 1 && node !== document.documentElement) {
      if (node !== element && node.id && unique(node.id)) { parts.unshift('#' + CSS.escape(node.id)); break; }
      let part = node.tagName.toLowerCase();
      const classes = Array.from(node.classList || []).filter(name => !name.startsWith('vinx-')).slice(0, 2);
      if (classes.length) part += '.' + classes.map(name => CSS.escape(name)).join('.');
      const parent = node.parentElement;
      if (parent) {
        const same = Array.from(parent.children).filter(child => child.tagName === node.tagName);
        if (same.length > 1) part += ':nth-of-type(' + (same.indexOf(node) + 1) + ')';
      }
      parts.unshift(part);
      node = parent;
    }
    return parts.join(' > ');
  }

  const docRect = rect => ({ x: Math.round(rect.left + scrollX), y: Math.round(rect.top + scrollY), w: Math.round(rect.width), h: Math.round(rect.height) });
  const viewport = () => ({ w: innerWidth, h: innerHeight });

  // ---- 点选 / 框选 ----
  let picking = null;
  function startPick() {
    if (picking) return;
    const layer = document.createElement('div');
    layer.setAttribute('style', `position:fixed;inset:0;z-index:${Z};cursor:crosshair;background:transparent`);
    const hover = document.createElement('div');
    hover.setAttribute('style', `position:absolute;z-index:${Z - 1};pointer-events:none;border:2px solid #e8590c;background:rgba(232,89,12,.12);border-radius:2px;display:none`);
    const box = document.createElement('div');
    box.setAttribute('style', `position:absolute;z-index:${Z - 1};pointer-events:none;border:2px dashed #e8590c;background:rgba(232,89,12,.08);display:none`);
    const tip = document.createElement('div');
    tip.textContent = '点一下要批注的元素，或按住拖出一个区域；Esc 取消';
    tip.setAttribute('style', `position:fixed;top:8px;left:50%;transform:translateX(-50%);z-index:${Z + 1};font:12px/1.4 sans-serif;` +
      'padding:6px 12px;border-radius:4px;background:#e8590c;color:#fff;pointer-events:none;box-shadow:0 2px 8px rgba(0,0,0,.25)');
    document.body.append(hover, box, layer, tip);
    let current = null, start = null;

    const under = (x, y) => { layer.style.pointerEvents = 'none'; const found = document.elementFromPoint(x, y); layer.style.pointerEvents = 'auto'; return found; };
    const place = (node, rect) => { Object.assign(node.style, { display: 'block', left: rect.x + 'px', top: rect.y + 'px', width: rect.w + 'px', height: rect.h + 'px' }); };

    layer.addEventListener('mousemove', event => {
      if (start && (Math.abs(event.clientX - start.x) > 8 || Math.abs(event.clientY - start.y) > 8)) {
        const x = Math.min(start.x, event.clientX), y = Math.min(start.y, event.clientY);
        place(box, { x: x + scrollX, y: y + scrollY, w: Math.abs(event.clientX - start.x), h: Math.abs(event.clientY - start.y) });
        hover.style.display = 'none';
        return;
      }
      current = under(event.clientX, event.clientY);
      if (current && current !== document.body && current !== document.documentElement) place(hover, docRect(current.getBoundingClientRect()));
      else hover.style.display = 'none';
    });
    layer.addEventListener('mousedown', event => { event.preventDefault(); start = { x: event.clientX, y: event.clientY }; });
    layer.addEventListener('mouseup', event => {
      const dragged = start && (Math.abs(event.clientX - start.x) > 8 || Math.abs(event.clientY - start.y) > 8);
      let anchor;
      if (dragged) {
        const x = Math.min(start.x, event.clientX), y = Math.min(start.y, event.clientY);
        const w = Math.abs(event.clientX - start.x), h = Math.abs(event.clientY - start.y);
        const center = under(x + w / 2, y + h / 2);
        anchor = { type: 'region', page, rect: { x: Math.round(x + scrollX), y: Math.round(y + scrollY), w: Math.round(w), h: Math.round(h) }, viewport: viewport() };
        if (center && center !== document.body && center !== document.documentElement) anchor.selector = selectorFor(center);
      } else {
        const target = under(event.clientX, event.clientY);
        if (!target) { start = null; return; }
        anchor = {
          type: 'element', page, selector: selectorFor(target),
          snippet: target.outerHTML.slice(0, 300),
          text: (target.innerText || target.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 120),
          rect: docRect(target.getBoundingClientRect()), viewport: viewport(),
        };
      }
      stopPick();
      post({ type: 'picked', anchor });
    });
    // 在层上滚动：把滚轮转给页面，方便边滚动边找元素。
    layer.addEventListener('wheel', event => { window.scrollBy(event.deltaX, event.deltaY); }, { passive: true });
    picking = { layer, hover, box, tip };
  }
  function stopPick() {
    if (!picking) return;
    Object.values(picking).forEach(node => node.remove());
    picking = null;
  }
  document.addEventListener('keydown', event => { if (event.key === 'Escape' && picking) { stopPick(); post({ type: 'pick-cancel' }); } }, true);

  // ---- 批注编号 ----
  let markers = [];
  let layerMarks = null;
  function drawMarkers() {
    if (!document.body) return;
    if (!layerMarks) {
      layerMarks = document.createElement('div');
      layerMarks.setAttribute('style', `position:absolute;left:0;top:0;width:0;height:0;z-index:${Z - 10};pointer-events:none`);
      document.body.append(layerMarks);
    }
    layerMarks.replaceChildren();
    for (const item of markers) {
      const anchor = item.anchor;
      let rect = anchor.rect;
      if (anchor.type === 'element' && anchor.selector) {
        try { const found = document.querySelector(anchor.selector); if (found) rect = docRect(found.getBoundingClientRect()); } catch (_) { /* 选择器失效时用记录的位置 */ }
      }
      if (!rect) continue;
      const color = item.status === 'sent' ? '#1971c2' : '#e8590c';
      if (anchor.type === 'region' || anchor.type === 'element') {
        const outline = document.createElement('div');
        outline.setAttribute('style', `position:absolute;left:${rect.x}px;top:${rect.y}px;width:${rect.w}px;height:${rect.h}px;` +
          `border:2px ${anchor.type === 'region' ? 'dashed' : 'solid'} ${color};border-radius:2px;opacity:.55;pointer-events:none`);
        outline.dataset.id = item.id;
        layerMarks.append(outline);
      }
      const dot = document.createElement('button');
      dot.type = 'button'; dot.textContent = item.id; dot.title = '批注 #' + item.id + '：' + item.body;
      dot.dataset.id = item.id;
      dot.setAttribute('style', `position:absolute;left:${Math.max(0, rect.x - 10)}px;top:${Math.max(0, rect.y - 10)}px;min-width:22px;height:22px;` +
        `padding:0 5px;border:2px solid #fff;border-radius:11px;background:${color};color:#fff;font:bold 11px/18px sans-serif;` +
        'cursor:pointer;pointer-events:auto;box-shadow:0 1px 4px rgba(0,0,0,.35)');
      dot.addEventListener('click', event => { event.stopPropagation(); post({ type: 'marker', id: item.id }); });
      layerMarks.append(dot);
    }
  }
  function focusMarker(id) {
    const dot = layerMarks && layerMarks.querySelector(`button[data-id="${id}"]`);
    if (!dot) return;
    dot.scrollIntoView({ block: 'center', behavior: 'smooth' });
    dot.animate([{ transform: 'scale(1)' }, { transform: 'scale(1.6)' }, { transform: 'scale(1)' }], { duration: 600, iterations: 2 });
  }
  let redraw = 0;
  const schedule = () => { clearTimeout(redraw); redraw = setTimeout(drawMarkers, 120); };
  window.addEventListener('resize', schedule);
  ready(() => { if (window.ResizeObserver) new ResizeObserver(schedule).observe(document.body); });

  window.addEventListener('message', event => {
    if (event.source !== window.parent || !event.data || event.data.vinx !== true) return;
    const data = event.data;
    if (data.type === 'pick') { if (data.on) startPick(); else stopPick(); }
    else if (data.type === 'markers' && Array.isArray(data.items)) { markers = data.items; ready(drawMarkers); }
    else if (data.type === 'focus') focusMarker(data.id);
  });
  ready(() => post({ type: 'ready', page }));
})();
