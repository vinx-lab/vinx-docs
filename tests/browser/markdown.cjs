// Markdown 短链接：跳到单文件阅读页渲染，图片和 Mermaid 可用，批注、编辑写回源文件，源文件修改后提示更新，页面列表里可预览。
// 用法：node tests/browser/markdown.cjs <服务地址> <短码> <源文件> <截图目录>；通常由 tests/browser/run.cjs 调用。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const [base, code, source, output] = process.argv.slice(2);
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
  const target = 'a:' + code + '/plan.md';
  try {
    // 1. 短链接跳到阅读页，正文、图片、Mermaid、清单外链接的说明都正常。
    await page.goto(base + '/a/' + code + '/');
    assert.equal(new URL(page.url()).pathname + new URL(page.url()).search, '/read.html?a=' + code);
    await page.locator('.markdown-section h1', { hasText: 'Markdown 方案' }).waitFor();
    await page.waitForFunction(() => { const img = document.querySelector('.markdown-section img'); return img && img.complete && img.naturalWidth > 0; });
    await page.locator('.markdown-section .mermaid svg').waitFor();
    assert.match(await page.locator('.markdown-section').textContent(), /未收录/);
    await page.screenshot({ path: output + '/markdown-reader.png' });

    // 1b. 本页目录在左侧：点目录项滚到对应标题（不是 404），标题在顶栏下面；可以收起和展开。
    const toc = page.locator('nav.page-toc-side');
    await toc.waitFor();
    const box = await toc.boundingBox();
    assert.ok(box.x < 40, '目录应在左侧：' + JSON.stringify(box));
    await toc.getByRole('link', { name: '取舍' }).click();
    await page.waitForFunction(() => { const h = document.getElementById('取舍'); const top = h && h.getBoundingClientRect().top; return top > 40 && top < 120; });
    assert.doesNotMatch(await page.locator('.markdown-section').textContent(), /404/);
    await page.waitForFunction(() => document.querySelector('nav.page-toc-side a[aria-current]')?.textContent === '取舍');
    // 二到四级标题成树：「方案」下的三级、四级标题可以折叠和展开。
    const sub = toc.getByRole('link', { name: '方案细节' });
    await page.getByRole('button', { name: '全部展开' }).click();
    await sub.waitFor();
    assert.ok(await toc.getByRole('link', { name: '第四级' }).isVisible());
    await page.getByRole('button', { name: '全部折叠' }).click();
    await sub.waitFor({ state: 'hidden' });
    await toc.getByRole('button', { name: '展开或折叠「方案」' }).click();
    await sub.waitFor();
    // 折叠状态下点有子项的目录项：跳过去并展开，滚动结束后仍保持展开。
    await page.getByRole('button', { name: '全部折叠' }).click();
    await sub.waitFor({ state: 'hidden' });
    await toc.getByRole('link', { name: '方案', exact: true }).click();
    await page.waitForFunction(() => { const top = document.getElementById('方案')?.getBoundingClientRect().top; return top > 40 && top < 120; });
    await page.waitForTimeout(1500);
    assert.ok(await sub.isVisible(), '点击后「方案」应保持展开');
    await sub.click();
    await page.waitForFunction(() => { const top = document.getElementById('方案细节')?.getBoundingClientRect().top; return top > 40 && top < 120; });
    await page.locator('.page-toc-hide').click();
    await toc.waitFor({ state: 'hidden' });
    await page.locator('.toc-toggle').click();
    await toc.waitFor();

    // 1c. 下载源文件：清单里有 Markdown 和它引用的图片，打成 zip，包内按原目录结构；可选按标题或按原文件名命名。
    await page.locator('#download-menu summary').click();
    assert.deepEqual(await page.locator('#download-menu .download-options a:visible small').allTextContents(), ['Markdown 方案.zip', 'plan.zip']);
    const [byFile] = await Promise.all([page.waitForEvent('download'), page.locator('#download-menu .download-options a').nth(1).click()]);
    assert.equal(byFile.suggestedFilename(), 'plan.zip');
    await page.locator('#download-menu summary').click();
    const [download] = await Promise.all([page.waitForEvent('download'), page.locator('#download-menu .download-options a').first().click()]);
    assert.equal(download.suggestedFilename(), 'Markdown 方案.zip');
    const zip = fs.readFileSync(await download.path());
    assert.equal(zip.readUInt32LE(0), 0x04034b50, '应是 zip');
    for (const name of ['Markdown 方案/plan.md', 'Markdown 方案/a.png']) assert.ok(zip.includes(Buffer.from(name)), '缺少 ' + name);

    // 2. 批注目标是短链接里的源文件。
    await page.locator('#comment-toggle').click();
    await page.getByRole('button', { name: '对整篇文档写批注' }).click();
    await page.locator('.vinx-compose textarea').fill('Markdown 短链接上的批注');
    await page.getByRole('button', { name: '保存批注', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.vinx-list')?.textContent.includes('Markdown 短链接上的批注'));
    const listed = await (await context.request.get(base + '/api/comments?target=' + encodeURIComponent(target))).json();
    assert.ok(listed.comments.some(c => c.body === 'Markdown 短链接上的批注'), JSON.stringify(listed));

    // 3. 编辑：保存写回源 Markdown。
    await page.locator('#edit-doc').click();
    await page.waitForFunction(() => document.querySelector('.vinx-editor .CodeMirror') && document.querySelector('.vinx-editor-state').textContent !== '正在读取…');
    await page.evaluate(() => { const cm = document.querySelector('.vinx-editor .CodeMirror').CodeMirror; cm.setValue(cm.getValue() + '\n页面上追加的一行。\n'); });
    await page.locator('.vinx-editor-bar button', { hasText: '保存' }).click();
    await page.waitForFunction(() => /已保存|保存成功|已同步/.test(document.querySelector('.vinx-editor-state').textContent));
    assert.match(fs.readFileSync(source, 'utf8'), /页面上追加的一行/);
    await Promise.all([page.waitForEvent('load'), page.locator('.vinx-editor-bar button', { hasText: '关闭' }).click()]);
    await page.waitForFunction(() => document.querySelector('.markdown-section')?.textContent.includes('页面上追加的一行'));

    // 4. 源文件在外部被修改后，阅读页提示更新。
    fs.appendFileSync(source, '\n外部追加的一行。\n');
    await page.locator('#update-notice').waitFor({ state: 'visible', timeout: 25000 });

    // 5. 页面列表里用阅读页预览（不在沙箱里，才能批注和编辑）。
    const list = await context.newPage();
    await list.goto(base + '/published.html#' + code);
    await list.frameLocator('#artifact-frame').locator('.markdown-section h1', { hasText: 'Markdown 方案' }).waitFor();
    assert.equal(await list.locator('#artifact-frame').getAttribute('sandbox'), null);
    assert.equal(await list.locator('#open-new').getAttribute('href'), '/read.html?a=' + code);
    assert.match(await list.locator('.published-row[aria-current="page"]').textContent(), /Markdown · /);
    await list.screenshot({ path: output + '/markdown-published.png' });
    await list.close();

    assert.deepEqual(errors, [], '页面报错');
    assert.deepEqual(external, [], '不应有外部请求');
    console.log('通过：短链接跳转阅读页、图片与 Mermaid、清单外链接说明、左侧本页目录跳转与收起、下载源文件、批注、编辑写回源文件、外部修改提示更新、页面列表预览。');
  } catch (error) {
    console.error(error && error.stack || error);
    process.exitCode = 1;
  } finally {
    await browser.close();
  }
})();
