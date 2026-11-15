#!/usr/bin/env node
// 对一个编译好的 vinx-docs 二进制跑全部真实浏览器测试：阅读页（browser.cjs）、
// 短链接/批注/编辑（vinx.cjs）、Markdown 短链接（markdown.cjs）、后台接入与同步（control-browser.cjs）、
// 发布到外部服务（share.cjs，用假 Google 服务）。
//
// 用法：
//   PLAYWRIGHT_MODULE=<playwright-core 模块目录> \
//   PLAYWRIGHT_EXECUTABLE=<Chromium 可执行文件> \
//   node tests/browser/run.cjs [vinx-docs 二进制路径] [--keep]
//
// 二进制路径也可以用环境变量 VINX_DOCS_BIN 给出，都没给时用 dist/vinx-docs（先跑 scripts/build.sh）。
// 夹具、家目录和截图都在系统临时目录下新建的 vinx-browser-* 目录里；服务只绑定 127.0.0.1，
// 端口由系统分配的空闲端口写进配置；家目录里不放 web/，用的是二进制内嵌的前端资源。
// 结束时按 PID 停掉自己启动的服务。全部通过时删除临时目录（--keep 保留），有失败时保留并打印路径。
'use strict';

const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');

const HERE = __dirname;
const REPO = path.resolve(HERE, '..', '..');

function fail(message) {
  console.error(message);
  process.exit(2);
}

const args = process.argv.slice(2);
const keep = args.includes('--keep');
const positional = args.filter((arg) => !arg.startsWith('--'));
const exe = process.platform === 'win32' ? 'vinx-docs.exe' : 'vinx-docs';
const binary = path.resolve(positional[0] || process.env.VINX_DOCS_BIN || path.join(REPO, 'dist', exe));
if (!fs.existsSync(binary)) fail(`找不到 vinx-docs 二进制：${binary}（先运行 scripts/build.sh，或用参数 / VINX_DOCS_BIN 指定）`);
for (const key of ['PLAYWRIGHT_MODULE', 'PLAYWRIGHT_EXECUTABLE']) {
  if (!process.env[key]) fail(`需要设置 ${key}`);
}

const WORK = fs.mkdtempSync(path.join(os.tmpdir(), 'vinx-browser-'));
const OUT = path.join(WORK, 'out');
fs.mkdirSync(OUT, { recursive: true });

function write(file, content) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, content);
}

// 服务要用的环境：只认临时家目录，清掉可能指向真实数据或改变监听地址的变量。
function envFor(home, extra = {}) {
  const env = { ...process.env, VINX_DOCS_HOME: home, ...extra };
  for (const key of ['VINX_DOCS_DB', 'VINX_DOCS_ASSETS', 'VINX_DOCS_LISTEN']) delete env[key];
  return env;
}

function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

// 直接连本机，不经过任何代理。
function localGet(url) {
  return new Promise((resolve) => {
    const request = http.get(url, { timeout: 3000 }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => resolve({ status: response.statusCode, body: Buffer.concat(chunks).toString('utf8') }));
    });
    request.on('timeout', () => request.destroy());
    request.on('error', () => resolve({ status: 0, body: '' }));
  });
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function writeConfig(home, port, projects) {
  write(path.join(home, 'config', 'projects.json'), JSON.stringify({
    schemaVersion: 2, server: { host: '127.0.0.1', port }, protectedPorts: [], projects,
  }, null, 2));
}

class Server {
  constructor(home, port, name, extra = {}) {
    this.port = port;
    this.logPath = path.join(WORK, `${name}-server.log`);
    this.log = fs.openSync(this.logPath, 'a');
    this.child = spawn(binary, ['serve'], { cwd: WORK, env: envFor(home, extra), stdio: ['ignore', this.log, this.log] });
    this.exited = false;
    this.child.on('exit', () => { this.exited = true; });
    this.child.on('error', () => { this.exited = true; });
  }

  async ready() {
    const deadline = Date.now() + 30000;
    while (Date.now() < deadline) {
      const { status, body } = await localGet(`http://127.0.0.1:${this.port}/api/status`);
      if (status === 200) {
        try {
          if (!JSON.parse(body).busy) return;
        } catch { /* 还没准备好 */ }
      }
      if (this.exited) throw new Error(`服务启动失败，见 ${this.logPath}`);
      await sleep(200);
    }
    throw new Error(`服务启动超时，见 ${this.logPath}`);
  }

  async stop() {
    if (!this.exited && this.child.pid) {
      const done = new Promise((resolve) => this.child.once('exit', resolve));
      try { process.kill(this.child.pid, 'SIGTERM'); } catch { /* 已退出 */ }
      const timer = setTimeout(() => { try { process.kill(this.child.pid, 'SIGKILL'); } catch { /* 已退出 */ } }, 15000);
      await done;
      clearTimeout(timer);
    }
    fs.closeSync(this.log);
  }
}

function runNode(script, scriptArgs, timeoutMs = 180000) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [script, ...scriptArgs], {
      cwd: WORK, env: { ...process.env, BROWSER_OUTPUT: OUT }, stdio: ['ignore', 'pipe', 'pipe'],
    });
    let output = '';
    child.stdout.on('data', (chunk) => { output += chunk; });
    child.stderr.on('data', (chunk) => { output += chunk; });
    const timer = setTimeout(() => { output += `\n超时（${timeoutMs / 1000} 秒），已终止`; child.kill('SIGKILL'); }, timeoutMs);
    child.on('close', (code) => {
      clearTimeout(timer);
      resolve({ code: code === null ? 1 : code, output: output.trim() });
    });
  });
}

// 编译并启动假 Google 服务，读第一行地址。先编译再运行，结束时能按 PID 停掉它。
function startFakeDrive() {
  const exe = path.join(WORK, 'fakedrive');
  const build = spawnSync('go', ['build', '-o', exe, './tests/browser/fakedrive'], { cwd: REPO, encoding: 'utf8' });
  if (build.status !== 0) return Promise.resolve({ child: { kill() {} }, output: String(build.error || build.stderr) });
  return new Promise((resolve) => {
    const child = spawn(exe, [], { cwd: WORK, stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    const timer = setTimeout(() => resolve({ child, output: output + '\n超时' }), 120000);
    child.on('error', (error) => { clearTimeout(timer); resolve({ child, output: String(error) }); });
    child.stderr.on('data', (chunk) => { output += chunk; });
    child.stdout.on('data', (chunk) => {
      output += chunk;
      const line = output.split('\n').find(l => l.startsWith('http://'));
      if (line) { clearTimeout(timer); resolve({ child, url: line.trim() }); }
    });
  });
}

function readerFixture(port) {
  const home = path.join(WORK, 'reader-home');
  const docs = path.join(WORK, 'reader-src', 'docs');
  write(path.join(docs, 'README.md'), '# 浏览器验证首页\n\n中文内容，见[使用说明](guide/使用.md)。\n\n## 小节一\n\n' + '正文。\n\n'.repeat(40) +
    "## 小节二\n\n```js\nconsole.log('中文对齐（测试）');\n```\n\n> [!NOTE]\n> 这是提示块。\n\n" + '更多正文。\n\n'.repeat(40));
  write(path.join(docs, 'guide', '使用.md'), '# 使用说明\n\n回到[首页](../README.md)。\n');
  write(path.join(docs, 'report.html'), "<h1>报告</h1><script>document.write('x')</script>");
  write(path.join(docs, '需求.xlsx'), Buffer.from('PK\x03\x04not-really-xlsx', 'latin1'));
  write(path.join(docs, '.env'), 'SECRET=1');
  writeConfig(home, port, [{ id: 'demo', name: '浏览器验证', docsPath: docs, home: 'README.md', exclude: [] }]);
  return home;
}

function pageFixture() {
  const page = path.join(WORK, 'page');
  write(path.join(page, 'index.html'), '<!doctype html><html><head><title>短链接页面</title><link rel="stylesheet" href="app.css"></head>' +
    '<body><h1>原型</h1><img src="a.png"></body></html>');
  write(path.join(page, 'app.css'), 'h1{color:#333}');
  write(path.join(page, 'a.png'), Buffer.from('89504e470d0a1a0a0000000d4948445200000001000000010806000000' +
    '1f15c4890000000d49444154789c6360000002000154a24f5d0000000049454e44ae426082', 'hex'));
  return path.join(page, 'index.html');
}

function markdownFixture() {
  const doc = path.join(WORK, 'markdown');
  write(path.join(doc, 'plan.md'), '# Markdown 方案\n\n![示意](a.png)\n\n```mermaid\ngraph LR\n  A --> B\n```\n\n见[另一篇](other.md)。\n' +
    ['背景', '方案', '取舍', '验证'].map(name => `\n## ${name}\n\n${'正文。'.repeat(400)}\n` +
      (name === '方案' ? `\n### 方案细节\n\n${'细节。'.repeat(300)}\n\n#### 第四级\n\n${'条目。'.repeat(300)}\n` : '')).join(''));
  write(path.join(doc, 'other.md'), '# 不在清单里\n');
  write(path.join(doc, 'a.png'), fs.readFileSync(path.join(WORK, 'page', 'a.png')));
  return path.join(doc, 'plan.md');
}

async function main() {
  const results = [];
  const record = (name, code, output) => results.push({ name, code, output });

  const readerPort = await freePort();
  const readerHome = readerFixture(readerPort);
  let server = new Server(readerHome, readerPort, 'reader');
  try {
    await server.ready();
    const base = `http://127.0.0.1:${readerPort}`;
    const reader = await runNode(path.join(HERE, 'browser.cjs'), [base]);
    record('tests/browser/browser.cjs（阅读页）', reader.code, reader.output);

    const publish = spawnSync(binary, ['publish', pageFixture(), '--json'], { cwd: WORK, env: envFor(readerHome), encoding: 'utf8' });
    let info = null;
    try { info = JSON.parse(publish.stdout); } catch { /* 下面报告 */ }
    if (!info || !info.id) {
      record('tests/browser/vinx.cjs（短链接/批注/编辑）', 1, `publish 失败（退出码 ${publish.status}）\n${publish.stdout}\n${publish.stderr}`);
    } else {
      const vinx = await runNode(path.join(HERE, 'vinx.cjs'), [base, info.id, OUT]);
      record(`tests/browser/vinx.cjs（短链接/批注/编辑，publish 退出码 ${publish.status}）`, vinx.code, vinx.output);
    }

    const markdown = markdownFixture();
    const md = spawnSync(binary, ['publish', markdown, '--json'], { cwd: WORK, env: envFor(readerHome), encoding: 'utf8' });
    let mdInfo = null;
    try { mdInfo = JSON.parse(md.stdout); } catch { /* 下面报告 */ }
    if (!mdInfo || !mdInfo.id) {
      record('tests/browser/markdown.cjs（Markdown 短链接）', 1, `publish 失败（退出码 ${md.status}）\n${md.stdout}\n${md.stderr}`);
    } else {
      const result = await runNode(path.join(HERE, 'markdown.cjs'), [base, mdInfo.id, markdown, OUT]);
      record(`tests/browser/markdown.cjs（Markdown 短链接，publish 退出码 ${md.status}）`, result.code, result.output);
    }
  } catch (error) {
    record('阅读服务', 1, String(error && error.message || error));
  } finally {
    await server.stop();
  }

  const controlPort = await freePort();
  const controlHome = path.join(WORK, 'control-home');
  writeConfig(controlHome, controlPort, []);
  const source = path.join(WORK, 'control-src', 'docs');
  write(path.join(source, 'README.md'), '# 首页\n\n中文内容');
  write(path.join(source, 'guide', '使用 说明.md'), '# 使用');
  write(path.join(source, '.private.md'), '# private');
  server = new Server(controlHome, controlPort, 'control');
  try {
    await server.ready();
    // control-browser.cjs 让浏览器把 docsify.test 解析到 127.0.0.1，用非 localhost 的主机名验证同源检查。
    const control = await runNode(path.join(HERE, 'control-browser.cjs'), [`http://docsify.test:${controlPort}`, source, OUT]);
    record('tests/browser/control-browser.cjs（后台接入与同步）', control.code, control.output);
  } catch (error) {
    record('后台服务', 1, String(error && error.message || error));
  } finally {
    await server.stop();
  }

  // 发布到外部服务：假 Google 服务（tests/browser/fakedrive，需要 Go）提供设备码授权和 Drive 接口。
  const fake = await startFakeDrive();
  if (!fake.url) {
    record('tests/browser/share.cjs（发布到外部服务）', 1, '假 Google 服务启动失败（需要 Go）：\n' + fake.output);
  } else {
    const sharePort = await freePort();
    const shareHome = path.join(WORK, 'share-home');
    writeConfig(shareHome, sharePort, []);
    const shareEnv = { VINX_GDRIVE_ENDPOINT: fake.url };
    // 外部适配器：第一次 publish 卡住（超时后结果未知），之后正常返回，用来验证「重试」。只能在命令行登记。
    const slow = path.join(WORK, 'slow-adapter');
    write(path.join(slow, 'adapter.json'), JSON.stringify({ id: 'slow', name: '慢适配器', version: '1', protocol: [1], command: ['x'],
      capabilities: { publicLink: false, privateLink: true, update: true, stableLinkOnUpdate: true, revoke: true } }));
    write(path.join(slow, 'adapter.sh'), '#!/bin/sh\nmarker="$VINX_SHARE_STATE_DIR/called"\n' +
      'if [ "$VINX_SHARE_METHOD" = publish ] && [ ! -f "$marker" ]; then touch "$marker"; sleep 30; fi\n' +
      'case "$VINX_SHARE_METHOD" in check) echo \'{"event":"result","data":{"account":"slow"}}\' ;; ' +
      'publish) echo \'{"event":"result","data":{"url":"https://example.invalid/slow","remoteId":"slow-1","public":false}}\' ;; ' +
      'revoke) echo \'{"event":"result","data":{"alreadyGone":false}}\' ;; esac\n');
    fs.chmodSync(path.join(slow, 'adapter.sh'), 0o755);
    const cli = (...cliArgs) => spawnSync(binary, cliArgs, { cwd: WORK, env: envFor(shareHome, shareEnv), encoding: 'utf8' });
    const shareMd = path.join(WORK, 'share-src', 'plan.md');
    write(shareMd, '# 发布测试\n\n![图](a.png)\n');
    write(path.join(WORK, 'share-src', 'a.png'), fs.readFileSync(path.join(WORK, 'page', 'a.png')));
    write(path.join(WORK, 'share-src', 'proto.html'), '<!doctype html><title>原型</title><h1>原型</h1>');
    const setup = [
      cli('share-target', 'add', 'slow', '--manifest', path.join(slow, 'adapter.json'), '--label', '慢目标', '--allow', 'pages',
        '--timeout', '2s', '--', path.join(slow, 'adapter.sh')),
    ];
    const mdPublish = cli('publish', shareMd, '--json');
    const htmlPublish = cli('publish', path.join(WORK, 'share-src', 'proto.html'), '--json');
    const bad = [...setup, mdPublish, htmlPublish].find(r => r.status !== 0 && r.status !== 3);
    if (bad) {
      record('tests/browser/share.cjs（发布到外部服务）', 1, `准备失败（退出码 ${bad.status}）\n${bad.stdout}\n${bad.stderr}`);
    } else {
      server = new Server(shareHome, sharePort, 'share', shareEnv);
      try {
        await server.ready();
        const shareResult = await runNode(path.join(HERE, 'share.cjs'), [`http://127.0.0.1:${sharePort}`, fake.url,
          JSON.parse(mdPublish.stdout).id, JSON.parse(htmlPublish.stdout).id, shareMd, OUT]);
        record('tests/browser/share.cjs（发布到外部服务）', shareResult.code, shareResult.output);
      } catch (error) {
        record('发布服务', 1, String(error && error.message || error));
      } finally {
        await server.stop();
      }
    }
    fake.child.kill();
  }

  let failed = 0;
  for (const { name, code, output } of results) {
    console.log(`== ${name}：${code === 0 ? '通过' : '失败'}`);
    if (output) console.log(output.slice(-3000));
    if (code !== 0) failed += 1;
  }
  console.log(`\n二进制：${binary}`);
  if (failed || keep) {
    console.log(`临时目录（日志、截图）：${WORK}`);
  } else {
    fs.rmSync(WORK, { recursive: true, force: true });
  }
  console.log(failed ? `失败 ${failed} 项，共 ${results.length} 项` : `全部通过（${results.length} 项）`);
  return failed ? 1 : 0;
}

main().then((code) => { process.exitCode = code; }, (error) => {
  console.error(error);
  console.error(`临时目录：${WORK}`);
  process.exitCode = 1;
});
