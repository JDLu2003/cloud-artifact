// artifact 内容 Worker：浏览器直接从 Cloudflare 读取 artifact 内容，不经过服务器。
//
// 路由与服务器的 /c/<id>/v<版本>/<令牌>/<路径> 完全一致，访问规则也一致：
//   - 令牌有效：可以看这个版本（包括私有、旧版本、回收站里的）；
//   - 没有令牌（"-"）：只能看公开或持链接可看、且是当前版本的 artifact；
//     版本已更新时打开入口会跳到查看页。
// 需要的数据由写节点写进 R2（见 internal/artifact/edgecontent.go）：
//   _m/<id>.json  _m/<id>/v<版本>.json  _r/<id>/v<版本>/<路径>  _m/_settings.json
//
// 绑定与变量：BUCKET（R2 桶）、CONTENT_KEY（artifactd content-key 的输出，secret）、
// PREFIX（与 ARTIFACT_S3_PREFIX 相同，可空）。

const enc = new TextEncoder();
const keyCache = new Map();
let settingsCache = { at: 0, value: null };

async function hmacKey(secret) {
  let k = keyCache.get(secret);
  if (!k) {
    k = crypto.subtle.importKey('raw', b64urlDecode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    keyCache.set(secret, k);
  }
  return k;
}

function b64urlDecode(s) {
  const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4));
  return Uint8Array.from(b, (c) => c.charCodeAt(0));
}

function b64urlEncode(bytes) {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function safeEqual(a, b) {
  if (a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

// verifyToken 与 Go 的 Signer.VerifyContentToken 一致：<过期时间 base36>.<HMAC 前 15 字节 base64url>
export async function verifyToken(secret, tok, id, version, nowSec = Math.floor(Date.now() / 1000)) {
  const dot = tok.indexOf('.');
  if (dot < 0 || !secret) return false;
  const exp = tok.slice(0, dot);
  const sig = tok.slice(dot + 1);
  if (!/^[0-9a-z]{1,13}$/i.test(exp)) return false;
  if (nowSec > parseInt(exp, 36)) return false;
  const mac = await crypto.subtle.sign('HMAC', await hmacKey(secret), enc.encode(['c', id, String(version), exp].join('|')));
  return safeEqual(sig, b64urlEncode(new Uint8Array(mac).slice(0, 15)));
}

async function getJSON(env, key) {
  const o = await env.BUCKET.get((env.PREFIX || '') + key);
  if (!o) return null;
  try { return JSON.parse(await o.text()); } catch { return null; }
}

async function settings(env) {
  const now = Date.now();
  if (!settingsCache.value || now - settingsCache.at > 60_000) {
    settingsCache = { at: now, value: (await getJSON(env, '_m/_settings.json')) || { cdn_allowlist: [], public_url: '' } };
  }
  return settingsCache.value;
}

function originOf(u) {
  try { return new URL(u).origin; } catch { return ''; }
}

// csp 与 Go 的 contentCSP 一致（来源 = 内容域名 + 主站）。
function csp(contentOrigin, st) {
  const origins = [contentOrigin];
  const pub = originOf(st.public_url);
  if (pub && pub.toLowerCase() !== contentOrigin.toLowerCase()) origins.push(pub);
  const self = "'self' " + origins.join(' ');
  const cdn = (st.cdn_allowlist || []).join(' ');
  return [
    'sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-pointer-lock',
    'default-src ' + self + ' data: blob:',
    'script-src ' + self + " 'unsafe-inline' 'unsafe-eval' blob: " + cdn,
    'style-src ' + self + " 'unsafe-inline' " + cdn,
    'font-src ' + self + ' data: ' + cdn,
    'img-src * data: blob:',
    'media-src * data: blob:',
    'connect-src ' + self + ' data: blob: ' + cdn,
    'worker-src ' + self + ' blob:',
    'frame-src ' + self + ' data: blob:',
    "form-action 'none'",
    'base-uri ' + self,
    'frame-ancestors ' + origins.join(' '),
  ].join('; ');
}

function contentHeaders(h, contentOrigin, st, signed) {
  h.set('Content-Security-Policy', csp(contentOrigin, st));
  h.set('Referrer-Policy', 'no-referrer');
  h.set('X-Content-Type-Options', 'nosniff');
  h.set('Cross-Origin-Resource-Policy', 'cross-origin');
  h.set('Access-Control-Allow-Origin', '*');
  h.set('Cache-Control', signed ? 'private, max-age=600' : 'public, max-age=60');
}

const text = (status, body, headers = new Headers()) => {
  headers.set('Content-Type', 'text/plain; charset=utf-8');
  return new Response(body, { status, headers });
};

function isText(mime) {
  return mime.startsWith('text/') || mime.includes('json') || mime.includes('xml') ||
    mime.includes('javascript') || mime.startsWith('image/svg');
}

function ext(p) {
  const i = p.lastIndexOf('.');
  return i > p.lastIndexOf('/') ? p.slice(i).toLowerCase() : '';
}

const ROUTE = /^\/c\/([A-Za-z0-9]{1,32})\/v([0-9]{1,9})\/([^/]+)(?:\/(.*))?$/;

export async function handle(req, env) {
  if (req.method !== 'GET' && req.method !== 'HEAD') return text(405, 'method not allowed');
  const url = new URL(req.url);
  const m = ROUTE.exec(url.pathname);
  if (!m) return text(404, 'not found');
  const [, id, verS, tok] = m;
  const ver = Number(verS);
  let p;
  try {
    p = (m[4] || '').split('/').filter((s) => s !== '').map(decodeURIComponent).join('/');
  } catch {
    return text(400, 'bad path');
  }
  if (p.split('/').some((s) => s === '..' || s === '.')) return text(404, 'not found');

  const meta = await getJSON(env, `_m/${id}.json`);
  if (!meta) return text(404, '404 page not found');
  const signed = tok !== '-' && (await verifyToken(env.CONTENT_KEY, tok, id, ver));
  const st = await settings(env);
  if (!signed && (meta.deleted || meta.visibility === 'private' || ver !== meta.current)) {
    if (!meta.deleted && meta.visibility !== 'private' && p === '') {
      return Response.redirect(`${st.public_url}/a/${id}`, 302);
    }
    return text(403, '链接已失效或没有权限。请从 artifact 的查看页重新打开。');
  }
  const man = await getJSON(env, `_m/${id}/v${ver}.json`);
  if (!man) return text(404, '404 page not found');
  if (p === '') p = man.entry;
  p = p.replace(/\/$/, '');
  let fp = p;
  let f = man.files[fp];
  if (!f) {
    fp = p ? p + '/index.html' : 'index.html';
    f = man.files[fp];
  }
  const headers = new Headers();
  contentHeaders(headers, url.origin, st, signed);
  if (!f) return text(404, '包里没有文件：' + p, headers);

  const raw = url.searchParams.has('raw');
  const e = ext(fp);
  const etag = `"${f.sha256.slice(0, 20)}"`;
  let key = `${id}/v${ver}/${fp}`;
  let type = f.mime;
  if (!raw && (e === '.md' || e === '.markdown')) {
    key = `_r/${id}/v${ver}/${fp}`;
    type = 'text/html; charset=utf-8';
  } else {
    if (req.headers.get('If-None-Match') === etag) return new Response(null, { status: 304, headers });
    headers.set('ETag', etag);
    if (raw && isText(f.mime)) {
      type = 'text/plain; charset=utf-8';
    } else if ((e === '.html' || e === '.htm') && f.rendered) {
      key = `_r/${id}/v${ver}/${fp}`;
    }
  }
  headers.set('Content-Type', type);
  headers.set('Accept-Ranges', 'bytes');
  const ranged = req.headers.has('Range') && !key.startsWith('_r/');
  const obj = await env.BUCKET.get((env.PREFIX || '') + key, ranged ? { range: req.headers } : undefined);
  if (!obj) return text(500, '读取内容失败');
  let status = 200;
  if (ranged && obj.range) {
    const size = obj.size;
    let off = obj.range.offset ?? (obj.range.suffix !== undefined ? size - obj.range.suffix : 0);
    let len = obj.range.length ?? size - off;
    if (obj.range.suffix !== undefined) { off = Math.max(0, size - obj.range.suffix); len = size - off; }
    status = 206;
    headers.set('Content-Range', `bytes ${off}-${off + len - 1}/${size}`);
    headers.set('Content-Length', String(len));
  } else if (typeof obj.size === 'number') {
    headers.set('Content-Length', String(obj.size));
  }
  return new Response(req.method === 'HEAD' ? null : obj.body, { status, headers });
}

export default {
  fetch(req, env) {
    return handle(req, env).catch(() => text(500, '内容服务暂时不可用'));
  },
};
