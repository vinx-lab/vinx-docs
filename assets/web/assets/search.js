/* 跨项目搜索：索引在生成站点时产出，浏览器端只做匹配和排序，不请求外部服务。 */
(function (global) {
  let index = null;
  let pending = null;

  function load() {
    if (index) return Promise.resolve(index);
    if (!pending) {
      pending = fetch('/search-index.json', { cache: 'no-store' })
        .then(response => {
          if (!response.ok) throw new Error('全局搜索索引不可用，请先同步文档。');
          return response.json();
        })
        .then(value => { index = value.documents || []; return index; })
        .catch(error => { pending = null; throw error; });
    }
    return pending;
  }

  function score(doc, terms) {
    const title = doc.title.toLowerCase();
    const path = doc.path.toLowerCase();
    const body = (doc.text || '').toLowerCase();
    let total = 0;
    for (const term of terms) {
      if (title.includes(term)) total += 10;
      else if (path.includes(term)) total += 6;
      else if (body.includes(term)) total += 2;
      else return 0;
    }
    return total;
  }

  function excerpt(doc, terms) {
    const body = doc.text || '';
    const at = body.toLowerCase().indexOf(terms[0]);
    if (at < 0) return body.slice(0, 110);
    return (at > 30 ? '…' : '') + body.slice(Math.max(0, at - 30), Math.max(0, at - 30) + 130);
  }

  async function query(text, options) {
    const terms = text.toLowerCase().split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    const documents = await load();
    const scope = options && options.project;
    return documents
      .filter(doc => !scope || doc.project === scope)
      .map(doc => ({ doc, weight: score(doc, terms) }))
      .filter(item => item.weight > 0)
      .sort((a, b) => b.weight - a.weight || b.doc.updatedAt - a.doc.updatedAt)
      .slice(0, (options && options.limit) || 20)
      .map(item => ({ ...item.doc, excerpt: excerpt(item.doc, terms) }));
  }

  global.DocsifyXSearch = { query, load };
})(window);
