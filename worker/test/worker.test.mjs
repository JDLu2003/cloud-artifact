// 用 Node 自带的测试运行器测试 Worker（不需要安装依赖）：node --test worker/test
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { handle, verifyToken } from '../src/index.js';

// 与 internal/auth/vector_test.go 相同的测试向量
const KEY = 'OoWwfmt_62y_XRnKEM7RefQAsVfLjorLkSwCMoYBaJk';
const TOKEN = 'zzzzzzz.axtZUD18vi0tiLsmBpAM';

// fakeBucket 模拟 R2 绑定的 get（含 range）。
function fakeBucket(objs) {
  return {
    async get(key, opts) {
      if (!(key in objs)) return null;
      const data = new TextEncoder().encode(objs[key]);
      let body = data;
      let range;
      const h = opts && opts.range;
      if (h && h.get('Range')) {
        const m = /bytes=(\d+)-(\d*)/.exec(h.get('Range'));
        const off = Number(m[1]);
        const end = m[2] ? Number(m[2]) : data.length - 1;
        range = { offset: off, length: end - off + 1 };
        body = data.slice(off, end + 1);
      }
      return { size: data.length, range, body, text: async () => objs[key] };
    },
  };
}

const files = {
  'index.html': { mime: 'text/html; charset=utf-8', sha256: 'a'.repeat(64), rendered: true },
  'doc.md': { mime: 'text/markdown; charset=utf-8', sha256: 'b'.repeat(64), rendered: true },
  'app.js': { mime: 'text/javascript; charset=utf-8', sha256: 'c'.repeat(64) },
  'img/x.bin': { mime: 'application/octet-stream', sha256: 'd'.repeat(64) },
  'sub/index.html': { mime: 'text/html; charset=utf-8', sha256: 'e'.repeat(64), rendered: true },
};

function env(meta) {
  return {
    CONTENT_KEY: KEY,
    PREFIX: 'p/',
    BUCKET: fakeBucket({
      'p/_m/_settings.json': JSON.stringify({ cdn_allowlist: ['https://cdn.example'], public_url: 'https://art.ex.com' }),
      'p/_m/abc123.json': JSON.stringify(meta),
      'p/_m/abc123/v3.json': JSON.stringify({ entry: 'index.html', files }),
      'p/_m/abc123/v2.json': JSON.stringify({ entry: 'index.html', files }),
      'p/abc123/v3/index.html': '<p>raw</p>',
      'p/_r/abc123/v3/index.html': '<!doctype html><p>wrapped</p>',
      'p/_r/abc123/v3/doc.md': '<h1>rendered</h1>',
      'p/abc123/v3/doc.md': '# raw md',
      'p/abc123/v3/app.js': 'console.log(1)',
      'p/abc123/v3/img/x.bin': '0123456789',
      'p/_r/abc123/v3/sub/index.html': 'sub',
      'p/_r/abc123/v2/index.html': 'old',
    }),
  };
}

const get = (e, path, headers = {}) => handle(new Request('https://content.ex.com' + path, { headers }), e);

test('令牌与 Go 一致', async () => {
  assert.equal(await verifyToken(KEY, TOKEN, 'abc123', 3), true);
  assert.equal(await verifyToken(KEY, TOKEN, 'abc123', 4), false);
  assert.equal(await verifyToken(KEY, TOKEN, 'abc124', 3), false);
  assert.equal(await verifyToken(KEY, '1.' + TOKEN.split('.')[1], 'abc123', 3), false); // 过期 / 签名不符
  assert.equal(await verifyToken(KEY, 'zzzzzzz.' + 'A'.repeat(20), 'abc123', 3), false);
});

test('公开 artifact 无令牌访问当前版本', async () => {
  const e = env({ visibility: 'public', current: 3, deleted: false });
  let r = await get(e, '/c/abc123/v3/-/');
  assert.equal(r.status, 200);
  assert.equal(await r.text(), '<!doctype html><p>wrapped</p>');
  assert.match(r.headers.get('Content-Security-Policy'), /frame-ancestors https:\/\/content\.ex\.com https:\/\/art\.ex\.com/);
  assert.match(r.headers.get('Content-Security-Policy'), /script-src .*https:\/\/cdn\.example/);
  assert.equal(r.headers.get('Cache-Control'), 'public, max-age=60');
  assert.equal(r.headers.get('ETag'), '"' + 'a'.repeat(20) + '"');

  r = await get(e, '/c/abc123/v3/-/doc.md');
  assert.equal(await r.text(), '<h1>rendered</h1>');
  assert.equal(r.headers.get('Content-Type'), 'text/html; charset=utf-8');
  r = await get(e, '/c/abc123/v3/-/doc.md?raw=1');
  assert.equal(await r.text(), '# raw md');
  assert.equal(r.headers.get('Content-Type'), 'text/plain; charset=utf-8');
  r = await get(e, '/c/abc123/v3/-/index.html?raw');
  assert.equal(await r.text(), '<p>raw</p>');
  r = await get(e, '/c/abc123/v3/-/app.js');
  assert.equal(r.headers.get('Content-Type'), 'text/javascript; charset=utf-8');
  r = await get(e, '/c/abc123/v3/-/sub/');
  assert.equal(await r.text(), 'sub');
  r = await get(e, '/c/abc123/v3/-/missing.png');
  assert.equal(r.status, 404);
  assert.match(await r.text(), /包里没有文件：missing\.png/);
  r = await get(e, '/c/abc123/v3/-/app.js', { 'If-None-Match': '"' + 'c'.repeat(20) + '"' });
  assert.equal(r.status, 304);
  r = await get(e, '/c/abc123/v3/-/img/x.bin', { Range: 'bytes=2-5' });
  assert.equal(r.status, 206);
  assert.equal(r.headers.get('Content-Range'), 'bytes 2-5/10');
  assert.equal(await r.text(), '2345');
  r = await get(e, '/c/abc123/v3/-/a%2f..%2fapp.js');
  assert.equal(r.status, 404);
});

test('旧版本、私有、删除需要令牌', async () => {
  let e = env({ visibility: 'public', current: 3, deleted: false });
  let r = await get(e, '/c/abc123/v2/-/');
  assert.equal(r.status, 302);
  assert.equal(r.headers.get('Location'), 'https://art.ex.com/a/abc123');
  r = await get(e, '/c/abc123/v2/-/index.html');
  assert.equal(r.status, 403);

  e = env({ visibility: 'private', current: 3, deleted: false });
  assert.equal((await get(e, '/c/abc123/v3/-/')).status, 403);
  r = await get(e, `/c/abc123/v3/${TOKEN}/`);
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('Cache-Control'), 'private, max-age=600');
  assert.equal((await get(e, `/c/abc123/v3/${TOKEN.replace('a', 'b')}/`)).status, 403);

  e = env({ visibility: 'public', current: 3, deleted: true });
  assert.equal((await get(e, '/c/abc123/v3/-/')).status, 403);
  assert.equal((await get(e, `/c/abc123/v3/${TOKEN}/`)).status, 200);
});

test('不存在的 artifact 与非法路由', async () => {
  const e = env({ visibility: 'public', current: 3, deleted: false });
  assert.equal((await get(e, '/c/nope/v3/-/')).status, 404);
  assert.equal((await get(e, '/a/abc123')).status, 404);
  assert.equal((await handle(new Request('https://content.ex.com/c/abc123/v3/-/', { method: 'POST' }), e)).status, 405);
});
