const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const modulePath = path.join(__dirname, '../assets/web/assets/links.js');
const manifest = { home: 'README.md', entries: [
  { path: 'README.md', kind: 'markdown', route: 'README.md', url: '/projects/demo/raw/README.md' },
  { path: '指南/中文 文件.md', kind: 'markdown', route: '指南/中文 文件.md', url: '/projects/demo/raw/x.md' },
  { path: 'data.json', kind: 'text', route: '__previews/data.json.md', url: '/projects/demo/raw/data.json' },
  { path: 'report.html', kind: 'html', route: null, url: '/projects/demo/raw/report.html', previewUrl: '/projects/demo/preview/report.html' },
  { path: '需求.xlsx', kind: 'download', route: null, url: '/projects/demo/raw/%E9%9C%80%E6%B1%82.xlsx' }
] };
function resolve(href, current = 'README.md') {
  assert.ok(fs.existsSync(modulePath), '链接解析模块尚未实现');
  return require(modulePath).resolveLink(href, current, manifest);
}
test('中文路径和标题锚点转换为项目内Docsify路由', () => {
  assert.deepEqual(resolve('指南/中文 文件.md#简介'), { type: 'document', href: '#/%E6%8C%87%E5%8D%97/%E4%B8%AD%E6%96%87%20%E6%96%87%E4%BB%B6.md?id=%E7%AE%80%E4%BB%8B' });
});
test('相对链接从源文档目录解析', () => {
  assert.equal(resolve('../README.md', '指南/中文 文件.md').href, '#/README.md');
  assert.equal(resolve('../data.json', '指南/中文 文件.md').href, '#/__previews/data.json.md');
});
test('HTML预览和Excel下载按允许条目分流', () => {
  assert.equal(resolve('report.html').type, 'html');
  assert.equal(resolve('report.html').href, '/projects/demo/preview/report.html');
  assert.equal(resolve('需求.xlsx').type, 'download');
});
test('越界、编码穿越、脚本链接和未注册文件被阻止', () => {
  for (const href of ['../README.md', '%2e%2e/README.md', 'javascript:alert(1)', '//evil.example/a', '/prod-api/getInfo', 'secret.env', 'foo\\README.md']) {
    assert.equal(resolve(href).type, 'blocked', href);
  }
});
test('标题链接、sidebar和原件地址不绕过允许清单', () => {
  assert.equal(resolve('#简介').href, '#/README.md?id=%E7%AE%80%E4%BB%8B');
  assert.equal(resolve('#/README.md').href, '#/README.md');
  assert.equal(resolve('#/not-published').type, 'blocked');
  assert.equal(resolve('/projects/demo/raw/data.json').type, 'download');
  assert.equal(resolve('/projects/other/raw/data.json').type, 'blocked');
});
test('外部HTTP链接只作为外链，不主动抓取', () => {
  assert.equal(resolve('https://example.com/reference').type, 'external');
});

test('工作簿优先给在线预览，并保留原件下载地址', () => {
  const workbook = {
    home: 'README.md',
    entries: [
      { path: 'README.md', kind: 'markdown', route: 'README.md', url: '/projects/p/raw/README.md' },
      {
        path: '需求/表格.xlsx', kind: 'download',
        url: '/projects/p/raw/%E9%9C%80%E6%B1%82/%E8%A1%A8%E6%A0%BC.xlsx',
        previewUrl: '/projects/p/sheet.html?f=%E9%9C%80%E6%B1%82%2F%E8%A1%A8%E6%A0%BC.xlsx',
      },
    ],
  };
  const links = require(modulePath);
  const hit = links.resolveLink('需求/表格.xlsx', 'README.md', workbook);
  assert.equal(hit.type, 'sheet');
  assert.equal(hit.href, '/projects/p/sheet.html?f=%E9%9C%80%E6%B1%82%2F%E8%A1%A8%E6%A0%BC.xlsx');
  assert.equal(links.resolveLink('缺失.xlsx', 'README.md', workbook).type, 'blocked');
});

test('生成的收录范围页按文档放行，未声明时仍阻止', () => {
  const links = require(modulePath);
  const withScope = { ...manifest, scopeRoute: '__scope.md' };
  assert.deepEqual(links.resolveLink('#/__scope.md', 'README.md', withScope), { type: 'document', href: '#/__scope.md' });
  assert.equal(links.resolveLink('#/__scope.md', 'README.md', manifest).type, 'blocked');
});
