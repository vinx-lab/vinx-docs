// 补充的浏览器检查：页面内编辑保存、文档批注、批注总览、短链接页面和发布列表。
// 用法：node tests/browser/vinx.cjs <服务地址> <短码> <截图目录>；通常由 tests/browser/run.cjs 调用。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const [base, code, output] = process.argv.slice(2);
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_EXECUTABLE, headless: true, chromiumSandbox: true, args: ['--no-proxy-server'] });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const errors = [], external = [];
  context.on('page', page => {
    page.on('pageerror', e => errors.push(page.url() + ' ' + e.message));
    page.on('request', r => { if (/^https?:/.test(r.url()) && new URL(r.url()).origin !== new URL(base).origin) external.push(r.url()); });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(8000);
  fs.mkdirSync(output, { recursive: true });
  try {
    // 1. 编辑：打开源文件编辑器，追加一行，保存后阅读页刷新出新内容。
    await page.goto(base + '/projects/demo/');
    await page.locator('.markdown-section h1').waitFor();
    await page.locator('#edit-doc').click();
    await page.waitForFunction(() => document.querySelector('.vinx-editor .CodeMirror') && document.querySelector('.vinx-editor-state').textContent !== '正在读取…');
    await page.evaluate(() => { const cm = document.querySelector('.vinx-editor .CodeMirror').CodeMirror; cm.setValue(cm.getValue() + '\n浏览器编辑追加的一行。\n'); });
    await page.locator('.vinx-editor-bar button', { hasText: '保存' }).click();
    await page.waitForFunction(() => /已保存|保存成功|已同步/.test(document.querySelector('.vinx-editor-state').textContent));
    const saved = await (await context.request.get(base + '/api/file?target=' + encodeURIComponent('doc:demo/README.md'))).json();
    assert.match(saved.content, /浏览器编辑追加的一行/);
    await page.screenshot({ path: output + '/vinx-editor.png' });
    await Promise.all([page.waitForEvent('load'), page.locator('.vinx-editor-bar button', { hasText: '关闭' }).click()]);
    await page.waitForFunction(() => document.querySelector('.markdown-section')?.textContent.includes('浏览器编辑追加的一行'));

    // 2. 批注：选中正文文字、点浮出的「批注」后，输入框自动获得焦点，可以直接输入。
    const paragraph = await page.locator('.markdown-section p').first().boundingBox();
    await page.mouse.move(paragraph.x + 2, paragraph.y + paragraph.height / 2);
    await page.mouse.down();
    await page.mouse.move(paragraph.x + 80, paragraph.y + paragraph.height / 2);
    await page.mouse.up();
    await page.locator('.vinx-select-btn').click();
    await page.keyboard.type('直接输入');
    assert.equal(await page.locator('.vinx-compose textarea').inputValue(), '直接输入');
    await page.getByRole('button', { name: '取消' }).click();
    await page.locator('#comment-toggle').click();  // 关上侧栏，下面照常从按钮打开

    // 对整篇文档写一条批注，抽屉里能看到。
    await page.locator('#comment-toggle').click();
    await page.getByRole('button', { name: '对整篇文档写批注' }).click();
    await page.locator('.vinx-compose textarea').fill('浏览器里写的批注');
    await page.getByRole('button', { name: '保存批注', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.vinx-list')?.textContent.includes('浏览器里写的批注'));
    await page.screenshot({ path: output + '/vinx-comment.png' });

    // 3. 批注总览页列出这条批注。
    const overview = await context.newPage();
    await overview.goto(base + '/comments.html');
    await overview.waitForFunction(() => document.getElementById('list').textContent.includes('浏览器里写的批注'));
    await overview.close();

    // 4. 短链接页面：沙箱里运行，注入的脚本能读版本号（Origin null），不会反复刷新。
    const short = await context.newPage();
    const response = await short.goto(base + '/a/' + code + '/');
    assert.equal(response.status(), 200);
    assert.match(response.headers()['content-security-policy'], /^sandbox allow-scripts/);
    await short.locator('h1').waitFor();
    const version = short.waitForResponse(r => r.url().endsWith('/__docsify_x/version'));
    await short.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
    const versionResponse = await version;
    assert.equal(versionResponse.status(), 200);
    assert.equal(versionResponse.headers()['access-control-allow-origin'], 'null');
    const before = await short.evaluate(() => performance.timeOrigin);
    await short.waitForTimeout(2500);
    assert.equal(await short.evaluate(() => performance.timeOrigin), before, '版本没变时不应刷新');
    assert.equal(await short.getByText('在 Vinx Docs 中批注').count(), 1);
    await short.screenshot({ path: output + '/vinx-short-link.png' });
    await short.close();

    // 5. 发布列表页：列出页面，并在框里打开。
    const list = await context.newPage();
    await list.goto(base + '/published.html#' + code);
    await list.waitForFunction(() => document.getElementById('artifact-list').textContent.includes('短链接页面'));
    await list.waitForFunction(code => document.getElementById('artifact-frame').getAttribute('src')?.includes('/a/' + code + '/'), code);
    await list.screenshot({ path: output + '/vinx-published.png' });
    await list.close();

    assert.deepEqual(errors, []);
    assert.deepEqual(external, []);
    console.log('通过：页面内编辑保存并刷新、文档批注、批注总览、短链接沙箱页面与版本检查、发布列表。');
  } finally {
    await context.close();
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
