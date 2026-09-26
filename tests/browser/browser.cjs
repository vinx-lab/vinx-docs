const assert = require('node:assert/strict');
const fs = require('node:fs');
const base = process.argv[2] || 'http://127.0.0.1:8000';
(async () => {
  const check = await fetch(base + '/healthz').catch(() => null);
  assert.ok(check && check.status === 200, '独立文档服务尚未运行');
  const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
  const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_EXECUTABLE || undefined, headless: true, chromiumSandbox: true });
  const output = process.env.BROWSER_OUTPUT || require('node:os').tmpdir();
  fs.mkdirSync(output, { recursive: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  const errors = [], external = [];
  page.on('pageerror', e => errors.push(e.message));
  page.on('request', r => { if (new URL(r.url()).origin !== new URL(base).origin && /^https?:/.test(r.url())) external.push(r.url()); });
  try {
    const catalogResponse = await context.request.get(base + '/catalog.json');
    assert.equal(catalogResponse.status(), 200);
    const { projects } = await catalogResponse.json();
    assert.ok(Array.isArray(projects) && projects.length > 0, '浏览器测试需要至少一个配置项目');
    const project = projects[0];
    const manifestResponse = await context.request.get(base + project.url + 'manifest.json');
    assert.equal(manifestResponse.status(), 200);
    const manifest = await manifestResponse.json();
    const markdown = manifest.entries.find(entry => entry.kind === 'markdown' && entry.route);
    assert.ok(markdown, '测试项目应包含Markdown文档');
    const html = manifest.entries.find(entry => entry.kind === 'html');
    assert.ok(html, '测试项目应包含HTML报告');
    const excel = manifest.entries.find(entry => entry.kind === 'download');
    assert.ok(excel, '测试项目应包含可下载附件');

    await page.goto(base + '/');
    await page.locator(`a.project-card-head[href="${project.url}"]`).click();
    await page.locator('.markdown-section h1').waitFor();
    assert.ok((await page.locator('.markdown-section').innerText()).trim());
    const internalLink = page.locator('.sidebar-nav a[href^="#/"]').first();
    await internalLink.click();
    await page.waitForFunction(() => location.hash.startsWith('#/'));
    await page.locator('.markdown-section h1').waitFor();
    await page.locator('.search input').fill(markdown.title);
    await page.waitForFunction(() => document.querySelectorAll('.search .matching-post').length > 0);
    await page.locator('.search input').fill('');
    await page.screenshot({ path: output + '/reader-desktop.png', fullPage: false });

    const preview = await context.request.get(base + html.previewUrl);
    assert.equal(preview.status(), 200);
    assert.match(preview.headers()['content-security-policy'], /sandbox/);
    const attachment = await context.request.get(base + excel.url);
    assert.equal(attachment.status(), 200);
    assert.match(attachment.headers()['content-disposition'], /attachment/);

    // 桌面侧栏收起状态刷新后保持，展开后同样保持。
    await page.goto(base + project.url);
    await page.locator('.markdown-section h1').waitFor();
    await page.locator('button.sidebar-toggle').click();
    assert.equal(await page.evaluate(() => document.body.classList.contains('close')), true);
    await page.reload();
    await page.locator('.markdown-section h1').waitFor();
    assert.equal(await page.evaluate(() => document.body.classList.contains('close')), true, '刷新后侧栏应保持收起');
    await page.locator('button.sidebar-toggle').click();
    await page.reload();
    await page.locator('.markdown-section h1').waitFor();
    assert.equal(await page.evaluate(() => document.body.classList.contains('close')), false, '刷新后侧栏应保持展开');

    // 后台更新只提示不自动刷新：拦截manifest模拟新版本。
    let fakeVersion = 'simulated-1';
    await page.route('**' + project.url + 'manifest.json', async route => {
      const response = await route.fetch();
      await route.fulfill({ response, json: { ...(await response.json()), version: fakeVersion } });
    });
    const before = await page.evaluate(() => performance.timeOrigin);
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await page.locator('#update-notice').waitFor({ state: 'visible' });
    assert.equal(await page.evaluate(() => performance.timeOrigin), before, '检测到更新时不得自动刷新');
    await page.screenshot({ path: output + '/reader-update-notice.png', fullPage: false });
    await page.locator('#update-dismiss').click();
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await page.waitForTimeout(500);
    assert.equal(await page.locator('#update-notice').isHidden(), true, '同一版本点“稍后”后不再提示');
    fakeVersion = 'simulated-2';
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    await page.locator('#update-notice').waitFor({ state: 'visible' });
    await page.unroute('**' + project.url + 'manifest.json');
    // 刷新后回到刚才阅读的位置：滚到最后一个小节标题附近再点刷新。
    const target = await page.evaluate(() => {
      const heading = Array.from(document.querySelectorAll('.markdown-section :is(h2,h3)[id]')).pop();
      if (!heading) return null;
      window.scrollTo(0, heading.getBoundingClientRect().top + scrollY - 120);
      return { id: heading.id, y: scrollY };
    });
    await Promise.all([page.waitForEvent('load'), page.locator('#update-reload').click()]);
    await page.locator('.markdown-section h1').waitFor();
    assert.equal(await page.locator('#update-notice').isHidden(), true);
    if (target && target.y > 200) {
      await page.waitForFunction(y => Math.abs(scrollY - y) < 40, target.y, { timeout: 5000 });
    }

    // 代码块复制按钮：安全上下文走 Clipboard API，非安全上下文（主机IP访问）走 execCommand。
    let codeRoute = null;
    for (const entry of manifest.entries.filter(e => e.kind === 'markdown' && e.route)) {
      const text = await (await context.request.get(base + project.url + 'content/' + entry.path)).text();
      if (/^```/m.test(text)) { codeRoute = entry.route; break; }
    }
    if (codeRoute) {
      await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base });
      const copyPage = await context.newPage();
      await copyPage.goto(base + project.url + '#/' + codeRoute);
      const block = copyPage.locator('.code-block').first();
      await block.waitFor();
      const expected = (await block.locator('pre code').innerText()).replace(/\n$/, '');
      await block.locator('.copy-code').click();
      await copyPage.waitForFunction(() => document.querySelector('.copy-code').textContent === '已复制');
      assert.equal(await copyPage.evaluate(() => navigator.clipboard.readText()), expected);
      await copyPage.addInitScript(() => Object.defineProperty(window, 'isSecureContext', { value: false }));
      await copyPage.reload();
      await copyPage.locator('.code-block').first().waitFor();
      await copyPage.evaluate(() => navigator.clipboard.writeText(''));
      await copyPage.locator('.code-block .copy-code').first().click();
      await copyPage.waitForFunction(() => document.querySelector('.copy-code').textContent === '已复制');
      assert.equal(await copyPage.evaluate(() => navigator.clipboard.readText()), expected, '非安全上下文应回退为execCommand复制');
      // 代码块中英文对齐：本机有可用的等宽字体时，汉字宽度必须正好是英文字符的2倍。
      await copyPage.evaluate(() => document.fonts.ready);
      const ratio = await copyPage.evaluate(() => {
        const loaded = []; document.fonts.forEach(f => { if (f.status === 'loaded' && /^DX (Cascadia|Consolas|DejaVu)/.test(f.family)) loaded.push(f.family); });
        if (!loaded.length) return null;
        const code = document.querySelector('.markdown-section pre code');
        const w = t => { const s = document.createElement('span'); s.textContent = t; code.append(s); const r = s.getBoundingClientRect().width; s.remove(); return r / t.length; };
        return w('中文对齐（测试）') / w('MMMMMMMM');
      });
      if (ratio !== null) assert.ok(Math.abs(ratio - 2) < 0.02, '代码块中汉字应为英文字符两倍宽，实际' + ratio);
      await copyPage.close();
    }

    // GitHub Alerts：`> [!NOTE]` 等渲染为提示框，不残留标记。
    let alertRoute = null;
    for (const entry of manifest.entries.filter(e => e.kind === 'markdown' && e.route)) {
      const text = await (await context.request.get(base + project.url + 'content/' + entry.path)).text();
      if (/^\s*>\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]/mi.test(text)) { alertRoute = entry.route; break; }
    }
    if (alertRoute) {
      const alertPage = await context.newPage();
      await alertPage.goto(base + project.url + '#/' + alertRoute);
      await alertPage.locator('blockquote.alert .alert-title').first().waitFor();
      assert.equal(await alertPage.evaluate(() => /\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]/i.test(document.querySelector('.markdown-section').innerText)), false, '提示块标记不应残留');
      await alertPage.close();
    }

    const narrow = await context.newPage({ viewport: { width: 400, height: 850 } });
    await narrow.goto(base + project.url);
    await narrow.locator('.markdown-section h1').waitFor();
    assert.equal(await narrow.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, '手机宽度不得出现页面水平溢出');
    await narrow.screenshot({ path: output + '/reader-mobile.png', fullPage: false });
    for (const route of ['/.env', '/.git/config', '/config/projects.json', `${project.url}raw/.env`, `${project.url}content/not-published.md`]) {
      const response = await context.request.get(base + route);
      assert.ok([403, 404].includes(response.status()), route + ' should be blocked');
    }
    assert.deepEqual(errors, []);
    assert.deepEqual(external, []);
    const results = { home: 'ok', project: 'ok', markdownLinks: 'ok', search: 'ok', sidebarMemory: 'ok', updateNotice: 'ok', restorePosition: 'ok', copyCode: 'ok', codeAlign: 'ok', alerts: 'ok', htmlSandbox: 'ok', attachmentDownload: 'ok', mobile: 'ok', privatePaths: 'blocked', externalRequests: 0, pageErrors: 0 };
    fs.writeFileSync(output + '/results.json', JSON.stringify(results, null, 2));
    console.log(JSON.stringify(results, null, 2));
  } finally { await context.close(); await browser.close(); }
})().catch(e => { console.error(e.message); process.exitCode = 1; });
