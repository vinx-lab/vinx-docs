// 发布到外部服务（spec 0008），全部在页面上完成，用假 Google 服务（tests/browser/fakedrive），不连真实网盘：
// 后台填写服务商设置（密钥不回显）、设备码授权连接两个账号、添加两个目标（公开的要二次确认）；
// 阅读页「发布到」列出目标并标明账号；发布、源文件改动后更新（链接不变）、撤回；公开目标的文件是公开的；
// 授权失效时提示去后台重新连接；外部适配器超时显示「结果未知」，重试成功；单独打开的 HTML 页面跳到页面列表并展开菜单。
// 用法：node tests/browser/share.cjs <服务地址> <假 Google 地址> <Markdown 短码> <HTML 短码> <Markdown 源文件> <截图目录>
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const [base, fakeUrl, mdCode, htmlCode, source, output] = process.argv.slice(2);
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_EXECUTABLE, headless: true, chromiumSandbox: true, args: ['--no-proxy-server'] });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  context.on('page', page => page.on('pageerror', e => errors.push(page.url() + ' ' + e.message)));
  const page = await context.newPage();
  page.setDefaultTimeout(10000);
  page.on('dialog', dialog => dialog.accept());
  fs.mkdirSync(output, { recursive: true });
  const status = page.locator('#share-status');
  const fakeFiles = async () => (await fetch(fakeUrl + '/__files')).json();

  // 在「手机」上完成授权：另开一个页面打开验证网址，输入验证码，点允许。
  async function connect(email) {
    await page.getByRole('button', { name: '连接 Google Drive 账号' }).click();
    const code = (await page.locator('.share-code').textContent()).trim();
    const url = await page.locator('.share-verify-url').getAttribute('href');
    const phone = await context.newPage();
    await phone.goto(url);
    await phone.fill('input[name=user_code]', code);
    await phone.fill('input[name=email]', email);
    await phone.click('button[value=allow]');
    await phone.locator('#result', { hasText: '已允许' }).waitFor();
    await phone.close();
    await page.locator('.share-item strong', { hasText: email }).waitFor({ timeout: 20000 });
  }

  async function addTarget(label, email, dir, isPublic) {
    await page.getByRole('button', { name: '新增目标' }).click();
    const form = page.locator('.share-target-form');
    await form.locator('input[name=label]').fill(label);
    await form.locator('select[name=account]').selectOption({ label: email });
    await form.locator('input[name="config.dir"]').fill(dir);
    if (isPublic) await form.locator('input[name=public]').check();
    await form.getByRole('button', { name: '添加目标' }).click();
    await status.filter({ hasText: '已保存：' + label }).waitFor();
  }

  const menu = page.locator('#share-menu');
  const dialogBox = page.locator('dialog.share-dialog');
  const openTarget = async label => {
    await menu.locator('summary').click();
    await menu.locator('.download-options a', { hasText: label }).click();
    await dialogBox.waitFor();
  };
  // 等任务结束：进度行消失，并出现预期的文字（上一次操作的结果会被替换掉）。
  const finished = async text => {
    await page.waitForFunction(source => {
      const body = document.querySelector('dialog.share-dialog .share-dialog-body');
      return body && !body.querySelector('.share-progress') && new RegExp(source).test(body.textContent);
    }, text.source, { timeout: 15000 });
  };
  const close = () => dialogBox.getByRole('button', { name: '关闭' }).click();

  try {
    // 1. 后台：服务商设置，密钥不回显。
    await page.goto(base + '/admin.html#share');
    const provider = page.locator('.share-provider');
    await provider.locator('input[name=clientId]').fill('fake-client-id');
    await provider.locator('input[name=clientSecret]').fill('fake-client-secret');
    await provider.getByRole('button', { name: '保存服务商设置' }).click();
    await status.filter({ hasText: '服务商设置已保存' }).waitFor();
    await page.locator('summary', { hasText: '服务商设置 · 已填写' }).click();
    assert.equal(await page.locator('.share-provider input[name=clientSecret]').inputValue(), '', '密钥不应回显');
    assert.equal(await page.locator('.share-provider input[name=clientSecret]').getAttribute('placeholder'), '已设置；留空不修改');

    // 2. 设备码授权连接两个账号，各加一个目标。
    await connect('alice@example.com');
    await connect('bob@example.com');
    await addTarget('Drive · 工作 / 客户A', 'alice@example.com', 'Vinx/客户A', false);
    await addTarget('Drive · 个人', 'bob@example.com', '', true);
    await page.screenshot({ path: output + '/share-admin.png', fullPage: true });
    const adminText = await page.locator('#share-admin').textContent();
    assert.doesNotMatch(adminText, /fake-client-secret/);

    // 3. 阅读页：菜单列出两个目标（另有命令行登记的慢目标），标明账号和是否公开。
    await page.goto(base + '/read.html?a=' + mdCode);
    await page.locator('.markdown-section h1').waitFor();
    await menu.waitFor();
    const labels = await menu.locator('.download-options a').allTextContents();
    assert.equal(labels.length, 3, labels.join(' | '));
    assert.ok(labels.some(text => /客户A/.test(text) && /alice@example\.com/.test(text) && /仅自己可见/.test(text)), labels.join(' | '));
    assert.ok(labels.some(text => /个人/.test(text) && /bob@example\.com/.test(text) && /公开链接/.test(text)), labels.join(' | '));

    // 4. 发布到 alice 的目标：显示链接；文件在 Vinx/客户A 里，非公开。
    await openTarget('客户A');
    assert.match(await dialogBox.textContent(), /2 个文件，打包成 zip/);
    await dialogBox.getByRole('button', { name: '发布' }).click();
    await finished(/已发布。/);
    const url = await dialogBox.locator('.share-link a').getAttribute('href');
    assert.match(url, /^https:\/\/drive\.google\.com\/file\/d\//);
    await page.screenshot({ path: output + '/share-published.png' });
    let files = await fakeFiles();
    const zip = files.find(f => f.owner === 'alice@example.com' && f.name === '发布测试.zip');
    const folder = files.find(f => f.id === zip.parents[0]);
    assert.equal(folder.name, '客户A');
    assert.equal(zip.public, false);
    await close();

    // 5. 源文件改动后提示「源文件已修改」，更新后链接不变；然后撤回（移到回收站）。
    fs.appendFileSync(source, '\n\n## 新增\n\n改过了。\n');
    await menu.locator('summary').click();
    await page.waitForFunction(() => [...document.querySelectorAll('#share-menu .download-options a')].some(a => /客户A/.test(a.textContent) && /源文件已修改/.test(a.textContent)));
    await menu.locator('.download-options a', { hasText: '客户A' }).click();
    await dialogBox.getByRole('button', { name: '更新' }).click();
    await finished(/已发布。/);
    assert.equal(await dialogBox.locator('.share-link a').getAttribute('href'), url, '更新后链接应不变');
    await dialogBox.getByRole('button', { name: '撤回' }).click();
    await finished(/已撤回。/);
    files = await fakeFiles();
    assert.equal(files.find(f => f.id === zip.id).trashed, true, '撤回应移到回收站');
    await close();

    // 6. 公开目标：文件是公开的。
    await openTarget('个人');
    await dialogBox.getByRole('button', { name: '发布' }).click();
    await finished(/已发布。/);
    files = await fakeFiles();
    assert.equal(files.find(f => f.owner === 'bob@example.com' && f.name === '发布测试.zip').public, true);
    await close();

    // 7. 授权失效：更新失败，提示去后台重新连接。
    await fetch(fakeUrl + '/__revoke-refresh', { method: 'POST' });
    fs.appendFileSync(source, '\n再改一次。\n');
    await openTarget('个人');
    await dialogBox.getByRole('button', { name: '更新' }).click();
    await finished(/需要重新授权/);
    await dialogBox.getByRole('link', { name: '去后台重新连接' }).waitFor();
    await close();
    await menu.locator('summary').click();
    assert.match(await menu.locator('.download-options a', { hasText: '个人' }).textContent(), /账号授权已失效/);
    await menu.locator('summary').click();

    // 8. 外部适配器超时：结果未知，重试成功。
    await openTarget('慢目标');
    await dialogBox.getByRole('button', { name: '发布' }).click();
    await finished(/结果未知/);
    await dialogBox.getByRole('button', { name: '重试' }).click();
    await finished(/已发布。/);
    await close();

    // 9. 单独打开的 HTML 页面：「发布到…」跳到页面列表，展开菜单让用户选目标。
    await page.goto(base + '/a/' + htmlCode + '/');
    await page.getByRole('link', { name: '发布到…' }).click();
    await page.waitForURL(/\/published\.html/);
    await page.waitForFunction(() => document.querySelector('#share-menu')?.open === true);
    assert.equal(await page.locator('dialog.share-dialog').count(), 0, '不应直接打开某个目标的发布框');

    assert.deepEqual(errors, []);
    console.log('share.cjs：全部通过');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
