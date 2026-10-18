// Artifact 管理后台。Preact + htm，无需构建。
import { html, render, useState, useEffect, useRef, useCallback } from '/_lib/preact/htm-preact.module.js';

// ---------- 工具 ----------

let CSRF = '';
let toastSet = null;

async function api(method, path, body) {
  const opt = { method, headers: {}, credentials: 'same-origin' };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  if (method !== 'GET') opt.headers['X-CSRF-Token'] = CSRF;
  const r = await fetch('/api/admin' + path, opt);
  let j = null;
  try { j = await r.json(); } catch (e) { /* 非 JSON */ }
  if (r.status === 401 && path !== '/login') {
    location.hash = '#/login';
    throw new Error('登录已过期，请重新登录');
  }
  if (!r.ok) throw new Error((j && j.error && j.error.message) || '请求失败（' + r.status + '）');
  return j;
}

function toast(msg, bad) { if (toastSet) toastSet({ msg, bad, t: Date.now() }); }

const fmtBytes = (n) => {
  if (n == null) return '—';
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB';
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB';
  return n + ' B';
};
const pad = (n) => String(n).padStart(2, '0');
const fmtTime = (ts) => {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};
const ago = (ts) => {
  if (!ts) return '从未';
  const s = Date.now() / 1000 - ts;
  if (s < 60) return '刚刚';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  if (s < 86400 * 30) return Math.floor(s / 86400) + ' 天前';
  return fmtTime(ts).slice(0, 10);
};
const fmtDur = (s) => {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d) return `${d} 天 ${h} 小时`;
  if (h) return `${h} 小时 ${m} 分`;
  return `${m} 分钟`;
};
const pct = (a, b) => (b ? Math.min(100, (a / b) * 100) : 0);
const VIS = { private: '私有', unlisted: '持链接可看', public: '公开' };
const KIND = { html: 'HTML', markdown: 'Markdown', svg: 'SVG' };
const SOURCE = { publish: '发布', edit: '局部编辑', restore: '恢复' };
const ACTIONS = {
  'artifact.create': '新建', 'artifact.publish': '发布新版本', 'artifact.edit': '局部编辑', 'artifact.restore': '恢复版本',
  'artifact.meta': '修改信息', 'artifact.delete': '移入回收站', 'artifact.undelete': '从回收站恢复', 'artifact.purge': '彻底删除',
  'key.issue': '签发密钥', 'key.revoke': '吊销密钥', 'user.create': '创建用户', 'user.update': '修改用户', 'user.delete': '删除用户',
  'user.password': '修改密码', login: '登录', 'login.fail': '登录失败', 'settings.update': '修改设置', gc: '清理文件', bootstrap: '初始化',
};

async function copy(text, el) {
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制');
  } catch (e) {
    if (el) {
      const range = document.createRange();
      range.selectNodeContents(el);
      const sel = getSelection(); sel.removeAllRanges(); sel.addRange(range);
    }
    toast('无法直接写入剪贴板，已选中文本，请手动复制', true);
  }
}

// ---------- 路由 ----------

function parseHash() {
  const h = location.hash.slice(1) || '/';
  const i = h.indexOf('?');
  return { path: i < 0 ? h : h.slice(0, i), q: new URLSearchParams(i < 0 ? '' : h.slice(i + 1)) };
}
function useRoute() {
  const [r, setR] = useState(parseHash());
  useEffect(() => {
    const f = () => setR(parseHash());
    addEventListener('hashchange', f);
    return () => removeEventListener('hashchange', f);
  }, []);
  return r;
}
function go(path, params) {
  const qs = params ? new URLSearchParams(Object.entries(params).filter(([, v]) => v !== '' && v != null)).toString() : '';
  location.hash = '#' + path + (qs ? '?' + qs : '');
}

function useLoad(fn, deps) {
  const [state, setState] = useState({ loading: true, data: null, error: null });
  const reload = useCallback(() => {
    setState((s) => ({ ...s, loading: true }));
    fn().then((data) => setState({ loading: false, data, error: null }), (error) => setState({ loading: false, data: null, error }));
  }, deps);
  useEffect(reload, [reload]);
  return [state, reload];
}

// ---------- 图标 ----------

const ICONS = {
  overview: 'M4 4h7v7H4zM13 4h7v4h-7zM13 10h7v10h-7zM4 13h7v7H4z',
  artifacts: 'M6 3h8l4 4v14H6zM14 3v4h4M9 12h6M9 16h6',
  keys: 'M15 7a4 4 0 1 1-3.9 4.9L4 19v-3h3v-3h3l1.1-1.1A4 4 0 0 1 15 7zM16 9.5h.01',
  users: 'M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM3 20c0-3.3 2.7-6 6-6s6 2.7 6 6M16 4.5a3.5 3.5 0 0 1 0 6.5M18 14.5c2 .8 3 2.8 3 5.5',
  settings: 'M4 7h10M18 7h2M4 17h4M12 17h8M14 4v6M8 14v6',
  audit: 'M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  account: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM5 21c0-3.9 3.1-7 7-7s7 3.1 7 7',
  external: 'M14 4h6v6M20 4l-9 9M18 14v6H4V6h6',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
};
const Icon = ({ name }) => html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d=${ICONS[name]} /></svg>`;
const Mark = () => html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" aria-hidden="true"><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/><path d="M9 12h6M9 15.5h6M9 19h3.5"/></svg>`;

// ---------- 通用组件 ----------

function Toast() {
  const [t, setT] = useState(null);
  toastSet = setT;
  useEffect(() => {
    if (!t) return;
    const id = setTimeout(() => setT(null), t.bad ? 5000 : 2200);
    return () => clearTimeout(id);
  }, [t]);
  return t ? html`<div class=${'toast' + (t.bad ? ' bad' : '')} role="status">${t.msg}</div>` : null;
}

// 二次确认按钮：第一次点击变成“确认……”，4 秒内再点才执行
function Confirm({ label, confirm, onConfirm, cls = 'btn danger sm', disabled }) {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const id = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(id);
  }, [armed]);
  const click = async (e) => {
    e.stopPropagation();
    if (!armed) { setArmed(true); return; }
    setArmed(false); setBusy(true);
    try { await onConfirm(); } catch (err) { toast(err.message, true); }
    setBusy(false);
  };
  return html`<button type="button" class=${cls + (armed ? ' armed' : '')} disabled=${busy || disabled} onClick=${click}>${busy ? '处理中…' : armed ? (confirm || '再点一次确认') : label}</button>`;
}

function Panel({ title, note, children, flush, actions }) {
  return html`<section class="panel">
    ${title && html`<header><h2>${title}</h2>${actions || (note && html`<span>${note}</span>`)}</header>`}
    <div class=${'body' + (flush ? ' flush' : '')}>${children}</div>
  </section>`;
}

function Meter({ value, max, warnAt = 75, badAt = 90 }) {
  const p = pct(value, max);
  const cls = p >= badAt ? 'meter bad' : p >= warnAt ? 'meter warn' : 'meter';
  return html`<div class=${cls} role="meter" aria-valuenow=${Math.round(p)} aria-valuemin="0" aria-valuemax="100"><i style=${{ width: p + '%' }}></i></div>`;
}

function Loading({ state, children }) {
  if (state.error) return html`<div class="alert bad">${state.error.message}</div>`;
  if (!state.data) return html`<div class="empty">正在加载…</div>`;
  return children(state.data);
}

function VisTag({ v }) {
  const cls = v === 'private' ? 'tag' : v === 'public' ? 'tag good' : 'tag accent';
  return html`<span class=${cls}>${VIS[v] || v}</span>`;
}

function Pager({ total, offset, limit, onPage }) {
  if (total <= limit) return null;
  const page = Math.floor(offset / limit) + 1, pages = Math.ceil(total / limit);
  return html`<div class="pager"><span class="num">第 ${page} / ${pages} 页，共 ${total} 条</span>
    <button class="btn sm" disabled=${page <= 1} onClick=${() => onPage(offset - limit)}>上一页</button>
    <button class="btn sm" disabled=${page >= pages} onClick=${() => onPage(offset + limit)}>下一页</button></div>`;
}

// 折线迷你图：请求数为面积，5xx 用红点标出
function Spark({ series }) {
  const W = 600, H = 64, n = series.length;
  const max = Math.max(1, ...series.map((s) => s.requests));
  const x = (i) => (i / (n - 1)) * W, y = (v) => H - (v / max) * (H - 4);
  const pts = series.map((s, i) => `${x(i).toFixed(1)},${y(s.requests).toFixed(1)}`).join(' ');
  return html`<svg class="spark" viewBox=${`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label=${'近 60 分钟每分钟请求数，最高 ' + max}>
    <polygon class="area" points=${`0,${H} ${pts} ${W},${H}`} />
    <polyline class="line" points=${pts} vector-effect="non-scaling-stroke" />
    ${series.map((s, i) => (s.errors ? html`<circle class="err" cx=${x(i)} cy=${y(s.requests)} r="3" />` : null))}
  </svg>`;
}

function Bars({ values, labels }) {
  const W = 420, H = 90, n = values.length, gap = 6, bw = (W - gap * (n - 1)) / n;
  const max = Math.max(1, ...values);
  return html`<svg class="bars" viewBox=${`0 0 ${W} ${H + 16}`} role="img" aria-label=${'近 ' + n + ' 天每天发布的版本数：' + values.join('、')} style="width:100%;height:auto">
    ${values.map((v, i) => {
      const h = v ? Math.max(3, (v / max) * H) : 2;
      return html`<g><rect class=${v ? '' : 'zero'} x=${i * (bw + gap)} y=${H - h} width=${bw} height=${h} rx="2"><title>${labels[i]}：${v} 次</title></rect>
        ${(i === 0 || i === n - 1 || i === Math.floor(n / 2)) && html`<text x=${i * (bw + gap) + bw / 2} y=${H + 13} text-anchor="middle">${labels[i]}</text>`}</g>`;
    })}
  </svg>`;
}

function dayLabels(n) {
  const out = [];
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(Date.now() - i * 86400000);
    out.push(`${d.getMonth() + 1}/${d.getDate()}`);
  }
  return out;
}

// ---------- 登录 ----------

function Login({ onLogin }) {
  const [u, setU] = useState('');
  const [p, setP] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true); setErr('');
    try {
      const r = await api('POST', '/login', { username: u, password: p });
      CSRF = r.csrf;
      const next = new URLSearchParams(location.search).get('next');
      if (next && next.startsWith('/') && !next.startsWith('//')) { location.href = next; return; }
      onLogin();
      location.hash = '#/';
    } catch (e2) { setErr(e2.message); }
    setBusy(false);
  };
  return html`<div class="login"><form onSubmit=${submit}>
    <h1><${Mark} />Artifact 管理后台</h1>
    <p>用管理员分配的用户名和密码登录。</p>
    <label class="f"><span>用户名</span><input class="i" id="login-user" autocomplete="username" value=${u} onInput=${(e) => setU(e.target.value)} required /></label>
    <label class="f"><span>密码</span><input class="i" id="login-pass" type="password" autocomplete="current-password" value=${p} onInput=${(e) => setP(e.target.value)} required /></label>
    ${err && html`<div class="alert bad">${err}</div>`}
    <button class="btn primary" type="submit" disabled=${busy}>${busy ? '正在登录…' : '登录'}</button>
  </form></div>`;
}

// ---------- 概览 ----------

function Overview({ me }) {
  const [st, reload] = useLoad(() => api('GET', '/overview'), []);
  useEffect(() => {
    if (me.user.role !== 'admin') return;
    const id = setInterval(reload, 30000);
    return () => clearInterval(id);
  }, [reload]);
  return html`<div class="stack">
    <div class="page-head"><div><h1>概览</h1><p>${me.user.role === 'admin' ? '系统运行状态和资源占用，每 30 秒刷新。' : '你的用量和最近的发布。'}</p></div>
      <button class="btn" onClick=${reload}>刷新</button></div>
    <${Loading} state=${st}>${(d) => (d.system ? html`<${AdminOverview} d=${d} />` : html`<${UserOverview} d=${d} />`)}<//>
  </div>`;
}

function AdminOverview({ d }) {
  const s = d.system, c = d.counts;
  const memMax = s.mem_limit || s.host_mem_total;
  const alerts = [];
  if (s.disk_free < d.min_free_disk_mb * 2 * 1048576) alerts.push(['bad', `磁盘只剩 ${fmtBytes(s.disk_free)}，低于 ${d.min_free_disk_mb} MB 时服务会拒绝写入。清理回收站或扩容。`]);
  if (s.mem_limit && s.mem_current / s.mem_limit > 0.85) alerts.push(['warn', `容器内存用到了上限的 ${Math.round((s.mem_current / s.mem_limit) * 100)}%。`]);
  const series = d.series || [];
  const total = series.reduce((a, b) => a + b.requests, 0), errs = series.reduce((a, b) => a + b.errors, 0), cli = series.reduce((a, b) => a + b.client, 0);
  const p95 = Math.max(0, ...series.map((x) => x.p95_ms || 0));
  return html`
    ${alerts.map(([k, m]) => html`<div class=${'alert ' + k}>${m}</div>`)}
    <div class="metrics">
      <div class="metric"><div class="k">运行时长</div><div class="v">${fmtDur(s.uptime)}</div><div class="s">${s.go_version}，${s.num_cpu} 核</div></div>
      <div class="metric"><div class="k">进程内存</div><div class="v">${fmtBytes(s.rss)}</div><div class="s">${s.mem_limit ? '容器上限 ' + fmtBytes(s.mem_limit) : '主机可用 ' + fmtBytes(s.host_mem_avail)}</div><${Meter} value=${s.mem_limit ? s.mem_current : s.host_mem_total - s.host_mem_avail} max=${memMax} /></div>
      <div class="metric"><div class="k">CPU</div><div class="v">${s.cpu.toFixed(1)}%</div><div class="s">负载 ${s.load1.toFixed(2)} / ${s.load5.toFixed(2)}</div></div>
      <div class="metric"><div class="k">磁盘可用</div><div class="v">${fmtBytes(s.disk_free)}</div><div class="s">共 ${fmtBytes(s.disk_total)}</div><${Meter} value=${s.disk_total - s.disk_free} max=${s.disk_total} warnAt=${85} badAt=${93} /></div>
      <div class="metric"><div class="k">数据</div><div class="v">${fmtBytes(c.blob_bytes)}</div><div class="s">${c.blobs} 个文件，数据库 ${fmtBytes(d.db_bytes)}</div></div>
    </div>
    <div class="metrics">
      <div class="metric"><div class="k">Artifact</div><div class="v num">${c.artifacts}</div><div class="s">回收站 ${c.trashed}</div></div>
      <div class="metric"><div class="k">版本</div><div class="v num">${c.versions}</div><div class="s">近 24 小时 ${c.publishes_today}</div></div>
      <div class="metric"><div class="k">浏览</div><div class="v num">${c.total_views}</div><div class="s">累计</div></div>
      <div class="metric"><div class="k">用户</div><div class="v num">${c.users}</div><div class="s">有效密钥 ${c.active_keys}</div></div>
    </div>
    <div class="grid-3">
      <${Panel} title="请求" note="近 60 分钟，每分钟">
        <${Spark} series=${series} />
        <div class="legend num"><span>共 ${total} 次</span><span>4xx ${cli} 次</span><span>5xx ${errs} 次（红点）</span><span>P95 延迟 ≤ ${p95 >= 5000 ? '5000+' : p95} ms</span></div>
      <//>
      <${Panel} title="发布" note="近 14 天，每天的版本数"><${Bars} values=${d.publishes_14d} labels=${dayLabels(14)} /><//>
    </div>
    <div class="grid-2">
      <${Panel} title="最近发布" flush>${html`<${RecentList} items=${d.recent} />`}<//>
      <${Panel} title="存储占用" note="按用户，去重后">
        ${d.top_users.length === 0 ? html`<div class="empty">还没有用户。</div>` : d.top_users.map((u) => html`<div class="usage-row">
          <span class="ellipsis">${u.name}</span><${Meter} value=${u.storage_bytes} max=${u.limit_bytes} /><span class="num faint nowrap">${fmtBytes(u.storage_bytes)} / ${fmtBytes(u.limit_bytes)}</span></div>`)}
      <//>
    </div>`;
}

function RecentList({ items }) {
  if (!items || !items.length) return html`<div class="empty">还没有发布过 artifact。</div>`;
  return html`<div class="table-wrap"><table class="t"><tbody>
    ${items.map((r) => html`<tr class="click" onClick=${() => go('/a/' + r.artifact_id)}>
      <td class="title-cell"><strong>${r.title}</strong><span class="faint">${r.message || SOURCE[r.source] || ''}</span></td>
      <td class="num nowrap">v${r.number}</td>
      <td class="nowrap muted">${r.author}</td>
      <td class="nowrap faint">${ago(r.created_at)}</td>
      <td>${r.warnings > 0 ? html`<span class="tag warn">${r.warnings} 个错误</span>` : ''}</td>
    </tr>`)}
  </tbody></table></div>`;
}

function UserOverview({ d }) {
  const m = d.me;
  return html`
    <div class="metrics">
      <div class="metric"><div class="k">存储</div><div class="v">${fmtBytes(m.storage_bytes)}</div><div class="s">上限 ${fmtBytes(m.quota.storage_bytes)}</div><${Meter} value=${m.storage_bytes} max=${m.quota.storage_bytes} /></div>
      <div class="metric"><div class="k">Artifact</div><div class="v num">${m.artifacts}</div><div class="s">上限 ${m.quota.artifacts} 个</div><${Meter} value=${m.artifacts} max=${m.quota.artifacts} /></div>
      <div class="metric"><div class="k">单文件上限</div><div class="v">${fmtBytes(m.quota.file_bytes)}</div><div class="s">单版本 ${fmtBytes(m.quota.version_bytes)}</div></div>
      <div class="metric"><div class="k">请求频率</div><div class="v num">${m.quota.req_per_min}</div><div class="s">次每分钟，发布 ${m.quota.publish_per_min} 次</div></div>
    </div>
    <div class="grid-3">
      <${Panel} title="最近发布" flush><${RecentList} items=${d.recent} /><//>
      <${Panel} title="发布" note="近 14 天"><${Bars} values=${m.publishes_14d} labels=${dayLabels(14)} /><//>
    </div>`;
}

// ---------- Artifact 列表 ----------

function useUsers(isAdmin) {
  const [users, setUsers] = useState([]);
  useEffect(() => {
    if (!isAdmin) return;
    api('GET', '/users').then((r) => setUsers(r.items.map((x) => x.user)), () => {});
  }, [isAdmin]);
  return users;
}

function Artifacts({ me, route, trash }) {
  const isAdmin = me.user.role === 'admin';
  const q = route.q;
  const f = { q: q.get('q') || '', owner: q.get('owner') || '', visibility: q.get('visibility') || '', kind: q.get('kind') || '', sort: q.get('sort') || '', offset: +(q.get('offset') || 0) };
  const [text, setText] = useState(f.q);
  const users = useUsers(isAdmin);
  const key = JSON.stringify(f) + (trash ? 't' : '');
  const [st, reload] = useLoad(() => {
    const p = new URLSearchParams({ limit: '50', offset: String(f.offset) });
    for (const k of ['q', 'owner', 'visibility', 'kind', 'sort']) if (f[k]) p.set(k, f[k]);
    if (trash) p.set('deleted', '1');
    return api('GET', '/artifacts?' + p);
  }, [key]);
  const base = trash ? '/trash' : '/artifacts';
  const set = (patch) => go(base, { ...f, offset: '', ...patch });
  const [gc, setGc] = useState(null);

  return html`<div class="stack">
    <div class="page-head"><div><h1>${trash ? '回收站' : 'Artifact'}</h1>
      <p>${trash ? '删除的 artifact 先放在这里，可以恢复；超过保留期后自动彻底删除。' : isAdmin ? '所有用户发布的 artifact。' : '你发布的 artifact。'}</p></div>
      ${trash && isAdmin && html`<div class="row">${gc && html`<span class="muted">${gc}</span>`}
        <button class="btn" onClick=${async () => { try { const r = await api('POST', '/gc'); setGc(`删除了 ${r.deleted} 个无引用文件，释放 ${fmtBytes(r.freed_bytes)}`); } catch (e) { toast(e.message, true); } }}>清理无引用文件</button></div>`}
    </div>
    ${!trash && html`<form class="row" onSubmit=${(e) => { e.preventDefault(); set({ q: text }); }}>
      <input class="i grow" style="max-width:340px" id="search" type="search" placeholder="搜索标题、描述、正文或 ID" value=${text} onInput=${(e) => setText(e.target.value)} />
      <button class="btn" type="submit">搜索</button>
      ${isAdmin && html`<select class="i" style="width:auto" id="f-owner" aria-label="所有者" value=${f.owner} onChange=${(e) => set({ owner: e.target.value })}>
        <option value="">全部用户</option>${users.map((u) => html`<option value=${u.id}>${u.name}</option>`)}</select>`}
      <select class="i" style="width:auto" id="f-vis" aria-label="可见性" value=${f.visibility} onChange=${(e) => set({ visibility: e.target.value })}>
        <option value="">全部可见性</option><option value="private">私有</option><option value="unlisted">持链接可看</option><option value="public">公开</option></select>
      <select class="i" style="width:auto" id="f-kind" aria-label="类型" value=${f.kind} onChange=${(e) => set({ kind: e.target.value })}>
        <option value="">全部类型</option><option value="html">HTML</option><option value="markdown">Markdown</option><option value="svg">SVG</option></select>
      <select class="i" style="width:auto" id="f-sort" aria-label="排序" value=${f.sort} onChange=${(e) => set({ sort: e.target.value })}>
        <option value="">最近更新</option><option value="created">最近创建</option><option value="views">浏览最多</option><option value="size">占用最大</option><option value="title">标题</option></select>
    </form>`}
    <${Loading} state=${st}>${(d) => html`<section class="panel">
      ${d.items.length === 0 ? html`<div class="empty">${trash ? '回收站是空的。' : f.q ? '没有找到匹配的 artifact，换个关键词试试。' : '还没有 artifact。把接入提示词交给 agent，让它发布第一个页面。'}</div>` : html`
      <div class="table-wrap"><table class="t">
        <thead><tr><th>标题</th>${isAdmin && html`<th>所有者</th>`}<th>可见性</th><th>类型</th><th class="n">版本</th><th class="n">大小</th><th class="n">浏览</th><th>${trash ? '删除于' : '更新于'}</th>${trash && html`<th></th>`}</tr></thead>
        <tbody>${d.items.map((a) => html`<tr class=${trash ? '' : 'click'} onClick=${() => !trash && go('/a/' + a.id)}>
          <td class="title-cell"><strong>${a.title}</strong><span class="mono faint">${a.id}</span></td>
          ${isAdmin && html`<td class="nowrap">${a.owner_name}</td>`}
          <td><${VisTag} v=${a.visibility} /></td>
          <td class="muted">${KIND[a.kind] || a.kind}</td>
          <td class="n">v${a.current_version}</td>
          <td class="n">${fmtBytes(a.size_bytes)}</td>
          <td class="n">${a.views}</td>
          <td class="nowrap muted">${ago(trash ? a.deleted_at : a.updated_at)}</td>
          ${trash && html`<td class="nowrap"><div class="row">
            <button class="btn sm" onClick=${async () => { try { await api('POST', `/artifacts/${a.id}/undelete`); toast('已恢复'); reload(); } catch (e) { toast(e.message, true); } }}>恢复</button>
            <${Confirm} label="彻底删除" confirm="确认彻底删除" onConfirm=${async () => { await api('DELETE', `/artifacts/${a.id}/purge`); toast('已彻底删除'); reload(); }} /></div></td>`}
        </tr>`)}</tbody>
      </table></div>`}
      <${Pager} total=${d.total} offset=${f.offset} limit=${50} onPage=${(o) => set({ offset: o })} />
    </section>`}<//>
  </div>`;
}

// ---------- Artifact 详情 ----------

function ArtifactDetail({ me, id }) {
  const isAdmin = me.user.role === 'admin';
  const [st, reload] = useLoad(() => api('GET', '/artifacts/' + id), [id]);
  const users = useUsers(isAdmin);
  return html`<${Loading} state=${st}>${(d) => html`<${ArtifactBody} d=${d} reload=${reload} users=${users} isAdmin=${isAdmin} />`}<//>`;
}

function ArtifactBody({ d, reload, users, isAdmin }) {
  const a = d.artifact;
  const [form, setForm] = useState({ title: a.title, description: a.description, visibility: a.visibility, owner_id: a.owner_id });
  useEffect(() => setForm({ title: a.title, description: a.description, visibility: a.visibility, owner_id: a.owner_id }), [a.id, a.updated_at]);
  const save = async (e) => {
    e.preventDefault();
    const body = { title: form.title, description: form.description, visibility: form.visibility };
    if (isAdmin && +form.owner_id !== a.owner_id) body.owner_id = +form.owner_id;
    try { await api('PATCH', '/artifacts/' + a.id, body); toast('已保存'); reload(); } catch (err) { toast(err.message, true); }
  };
  const rollback = async (n) => {
    const r = await api('POST', `/artifacts/${a.id}/rollback`, { version: n });
    toast(`已用 v${n} 的内容生成 v${r.version}`);
    reload();
  };
  return html`<div class="stack">
    <div class="page-head"><div class="grow"><h1 class="ellipsis">${a.title}</h1>
      <p class="row"><span class="mono">${a.id}</span><${VisTag} v=${a.visibility} /><span>${a.owner_name}</span><span>v${a.current_version}</span><span>${fmtBytes(a.size_bytes)}</span><span>浏览 ${a.views} 次</span>${a.deleted_at && html`<span class="tag bad">在回收站</span>`}</p></div>
      <div class="row"><a class="btn" href=${d.url} target="_blank" rel="noopener"><${Icon} name="external" />查看页</a>
        <a class="btn" href=${'/a/' + a.id + '/history'} target="_blank" rel="noopener">历史</a>
        <a class="btn" href=${'/a/' + a.id + '/source'} target="_blank" rel="noopener">源码</a></div>
    </div>
    <div class="grid-3">
      <div class="stack">
        <${Panel} title="预览" note="当前版本">
          <iframe class="preview" src=${d.preview} title=${'预览：' + a.title} sandbox="allow-scripts allow-forms allow-popups allow-modals" referrerpolicy="no-referrer"></iframe>
        <//>
        <${Panel} title="版本" note=${d.versions.length + ' 个'} flush>
          <div class="table-wrap"><table class="t">
            <thead><tr><th>版本</th><th>时间</th><th>说明</th><th>方式</th><th>作者</th><th class="n">大小</th><th></th></tr></thead>
            <tbody>${d.versions.map((v) => html`<tr>
              <td class="nowrap"><a href=${'/a/' + a.id + '?v=' + v.number} target="_blank" rel="noopener">v${v.number}</a>${v.number === a.current_version && html` <span class="tag accent">当前</span>`}</td>
              <td class="nowrap muted num">${fmtTime(v.created_at)}</td>
              <td>${v.message || html`<span class="faint">未填写</span>`}${v.warnings > 0 && html` <span class="tag warn">${v.warnings} 个错误</span>`}</td>
              <td class="nowrap">${SOURCE[v.source] || v.source}</td>
              <td class="nowrap muted">${v.author_name}</td>
              <td class="n">${fmtBytes(v.total_size)}</td>
              <td class="nowrap"><div class="row">
                ${v.number > 1 && html`<a class="btn sm ghost" href=${`/a/${a.id}/diff?from=${v.number - 1}&to=${v.number}`} target="_blank" rel="noopener">对比上一版</a>`}
                ${v.number !== a.current_version && !a.deleted_at && html`<${Confirm} label="恢复" confirm=${'确认恢复 v' + v.number} cls="btn sm" onConfirm=${() => rollback(v.number)} />`}
              </div></td></tr>`)}</tbody>
          </table></div>
        <//>
      </div>
      <div class="stack">
        <${Panel} title="信息">
          <form class="stack" onSubmit=${save}>
            <label class="f"><span>标题</span><input class="i" id="a-title" value=${form.title} onInput=${(e) => setForm({ ...form, title: e.target.value })} maxlength="120" /></label>
            <label class="f"><span>描述</span><textarea class="i" id="a-desc" style="font-family:var(--font);font-size:13.5px;min-height:70px" value=${form.description} onInput=${(e) => setForm({ ...form, description: e.target.value })} maxlength="500"></textarea></label>
            <label class="f"><span>可见性</span><select class="i" id="a-vis" value=${form.visibility} onChange=${(e) => setForm({ ...form, visibility: e.target.value })}>
              <option value="private">私有：只有所有者和管理员能看</option><option value="unlisted">持链接可看：拿到链接的人都能看</option><option value="public">公开：还会出现在首页</option></select></label>
            ${isAdmin && html`<label class="f"><span>所有者</span><select class="i" id="a-owner" value=${form.owner_id} onChange=${(e) => setForm({ ...form, owner_id: e.target.value })}>
              ${users.map((u) => html`<option value=${u.id}>${u.name}</option>`)}</select></label>`}
            <div class="row"><button class="btn primary" type="submit" disabled=${!!a.deleted_at}>保存信息</button></div>
          </form>
        <//>
        <${Panel} title="文件" note=${'v' + a.current_version + '，' + d.files.length + ' 个'} flush>
          <div class="table-wrap"><table class="t"><tbody>${d.files.map((f) => html`<tr>
            <td class="mono" style="word-break:break-all"><a href=${`/a/${a.id}/source?path=${encodeURIComponent(f.path)}`} target="_blank" rel="noopener">${f.path}</a>${f.path === a.entry && html` <span class="tag accent">入口</span>`}</td>
            <td class="n">${fmtBytes(f.size)}</td></tr>`)}</tbody></table></div>
        <//>
        <${Panel} title="删除">
          <p class="muted" style="margin:0 0 10px">移入回收站后，链接立即失效；所有者和管理员可以在回收站恢复。</p>
          ${a.deleted_at ? html`<span class="muted">已在回收站。</span>` : html`<${Confirm} label="移入回收站" confirm="确认移入回收站" onConfirm=${async () => { await api('DELETE', '/artifacts/' + a.id); toast('已移入回收站'); go('/artifacts'); }} />`}
        <//>
      </div>
    </div>
  </div>`;
}

// ---------- 密钥 ----------

function NewSecret({ data, onClose }) {
  const keyRef = useRef(), promptRef = useRef();
  return html`<${Panel} title="新密钥已签发" note="只显示这一次，关闭后无法再查看">
    <div class="stack">
      <div class="secret"><code ref=${keyRef}>${data.key}</code><button class="btn sm" onClick=${() => copy(data.key, keyRef.current)}><${Icon} name="copy" />复制</button></div>
      <div><div class="row" style="justify-content:space-between;margin-bottom:6px"><strong>接入提示词</strong>
        <button class="btn sm" onClick=${() => copy(data.prompt, promptRef.current)}><${Icon} name="copy" />复制提示词</button></div>
        <pre class="box" ref=${promptRef}>${data.prompt}</pre>
        <p class="muted" style="margin:8px 0 0">把这段话整体粘贴给 agent（pi、Claude Code、Codex 等），它会自己下载技能、保存凭据并验证。</p></div>
      <div><button class="btn" onClick=${onClose}>我已保存，关闭</button></div>
    </div>
  <//>`;
}

function Keys({ me, route }) {
  const isAdmin = me.user.role === 'admin';
  const userFilter = route.q.get('user') || '';
  const users = useUsers(isAdmin);
  const [st, reload] = useLoad(() => api('GET', '/keys' + (userFilter ? '?user=' + userFilter : '')), [userFilter]);
  const [form, setForm] = useState({ user_id: '', name: '', expires_days: '' });
  const [secret, setSecret] = useState(null);
  const create = async (e) => {
    e.preventDefault();
    try {
      const r = await api('POST', '/keys', { user_id: +(form.user_id || me.user.id), name: form.name, expires_days: +(form.expires_days || 0) });
      setSecret(r); setForm({ ...form, name: '' }); reload();
    } catch (err) { toast(err.message, true); }
  };
  const now = Date.now() / 1000;
  return html`<div class="stack">
    <div class="page-head"><div><h1>密钥</h1><p>每个 agent 用一把单独的密钥，方便单独吊销。密钥只在签发时显示一次。</p></div></div>
    ${secret && html`<${NewSecret} data=${secret} onClose=${() => setSecret(null)} />`}
    ${me.allow_self_keys ? html`<${Panel} title="签发新密钥">
      <form class="form-grid" onSubmit=${create} style="align-items:end">
        ${isAdmin && html`<label class="f"><span>给哪个用户</span><select class="i" id="k-user" value=${form.user_id} onChange=${(e) => setForm({ ...form, user_id: e.target.value })}>
          <option value="">我自己（${me.user.name}）</option>${users.filter((u) => u.id !== me.user.id).map((u) => html`<option value=${u.id}>${u.name}</option>`)}</select></label>`}
        <label class="f"><span>名称 <small>用来区分是哪个 agent</small></span><input class="i" id="k-name" placeholder="例如：笔记本上的 pi" value=${form.name} onInput=${(e) => setForm({ ...form, name: e.target.value })} maxlength="60" /></label>
        <label class="f"><span>有效期 <small>天，留空为长期</small></span><input class="i" id="k-exp" type="number" min="1" value=${form.expires_days} onInput=${(e) => setForm({ ...form, expires_days: e.target.value })} /></label>
        <div><button class="btn primary" type="submit">签发</button></div>
      </form>
    <//>` : html`<div class="alert info">管理员关闭了自助签发密钥。需要新密钥时请联系管理员。</div>`}
    ${isAdmin && html`<div class="row"><select class="i" style="width:auto" id="k-filter" aria-label="按用户筛选" value=${userFilter} onChange=${(e) => go('/keys', { user: e.target.value })}>
      <option value="">全部用户的密钥</option>${users.map((u) => html`<option value=${u.id}>${u.name}</option>`)}</select></div>`}
    <${Loading} state=${st}>${(d) => html`<section class="panel">${d.items.length === 0 ? html`<div class="empty">还没有密钥。</div>` : html`
      <div class="table-wrap"><table class="t">
        <thead><tr><th>名称</th>${isAdmin && html`<th>用户</th>`}<th>前缀</th><th>状态</th><th>最后使用</th><th>签发于</th><th>过期</th><th></th></tr></thead>
        <tbody>${d.items.map((k) => {
          const revoked = !!k.revoked_at, expired = k.expires_at && k.expires_at <= now;
          return html`<tr>
            <td>${k.name}</td>${isAdmin && html`<td>${k.user_name}</td>`}
            <td class="mono">${k.prefix}…</td>
            <td>${revoked ? html`<span class="tag">已吊销</span>` : expired ? html`<span class="tag warn">已过期</span>` : html`<span class="tag good">有效</span>`}</td>
            <td class="nowrap muted">${ago(k.last_used_at)}${k.last_ip && html` <span class="faint mono">${k.last_ip}</span>`}</td>
            <td class="nowrap muted num">${fmtTime(k.created_at)}</td>
            <td class="nowrap muted num">${k.expires_at ? fmtTime(k.expires_at) : '长期'}</td>
            <td>${!revoked && html`<${Confirm} label="吊销" confirm="确认吊销" onConfirm=${async () => { await api('DELETE', '/keys/' + k.id); toast('已吊销'); reload(); }} />`}</td>
          </tr>`;
        })}</tbody></table></div>`}</section>`}<//>
  </div>`;
}

// ---------- 用户 ----------

function Users({ me }) {
  const [st, reload] = useLoad(() => api('GET', '/users'), []);
  const [form, setForm] = useState({ name: '', display_name: '', password: '', role: 'user', issue_key: true });
  const [open, setOpen] = useState(false);
  const [secret, setSecret] = useState(null);
  const create = async (e) => {
    e.preventDefault();
    try {
      const r = await api('POST', '/users', form);
      toast('已创建用户 ' + r.user.name);
      if (r.key) setSecret(r);
      setForm({ name: '', display_name: '', password: '', role: 'user', issue_key: true }); setOpen(false); reload();
    } catch (err) { toast(err.message, true); }
  };
  return html`<div class="stack">
    <div class="page-head"><div><h1>用户</h1><p>用户名和密码用于登录后台；agent 使用密钥访问接口。</p></div>
      <button class="btn primary" onClick=${() => setOpen(!open)}>${open ? '取消' : '新建用户'}</button></div>
    ${secret && html`<${NewSecret} data=${secret} onClose=${() => setSecret(null)} />`}
    ${open && html`<${Panel} title="新建用户"><form class="stack" onSubmit=${create}>
      <div class="form-grid">
        <label class="f"><span>用户名 <small>字母、数字、- _ .</small></span><input class="i" id="u-name" required value=${form.name} onInput=${(e) => setForm({ ...form, name: e.target.value })} /></label>
        <label class="f"><span>显示名</span><input class="i" id="u-display" value=${form.display_name} onInput=${(e) => setForm({ ...form, display_name: e.target.value })} /></label>
        <label class="f"><span>密码 <small>留空则不能登录后台</small></span><input class="i" id="u-pass" type="password" autocomplete="new-password" value=${form.password} onInput=${(e) => setForm({ ...form, password: e.target.value })} /></label>
        <label class="f"><span>角色</span><select class="i" id="u-role" value=${form.role} onChange=${(e) => setForm({ ...form, role: e.target.value })}><option value="user">普通用户</option><option value="admin">管理员</option></select></label>
      </div>
      <label class="check"><input type="checkbox" id="u-key" checked=${form.issue_key} onChange=${(e) => setForm({ ...form, issue_key: e.target.checked })} />同时签发一把密钥并生成接入提示词</label>
      <div><button class="btn primary" type="submit">创建</button></div>
    </form><//>`}
    <${Loading} state=${st}>${(d) => html`<section class="panel"><div class="table-wrap"><table class="t">
      <thead><tr><th>用户</th><th>角色</th><th>状态</th><th class="n">Artifact</th><th>存储</th><th class="n">密钥</th><th>最近活动</th></tr></thead>
      <tbody>${d.items.map((r) => html`<tr class="click" onClick=${() => go('/users/' + r.user.id)}>
        <td><strong>${r.user.name}</strong>${r.user.display_name && html` <span class="muted">${r.user.display_name}</span>`}</td>
        <td>${r.user.role === 'admin' ? html`<span class="tag accent">管理员</span>` : '普通用户'}</td>
        <td>${r.user.disabled ? html`<span class="tag bad">已停用</span>` : html`<span class="tag good">正常</span>`}</td>
        <td class="n">${r.artifacts} / ${r.quota.artifacts}</td>
        <td style="min-width:180px"><div class="row" style="flex-wrap:nowrap"><div class="grow"><${Meter} value=${r.storage_bytes} max=${r.quota.storage_bytes} /></div><span class="num faint nowrap">${fmtBytes(r.storage_bytes)} / ${fmtBytes(r.quota.storage_bytes)}</span></div></td>
        <td class="n">${r.keys}</td>
        <td class="nowrap muted">${ago(r.last_active_at)}</td>
      </tr>`)}</tbody></table></div></section>`}<//>
  </div>`;
}

function UserDetail({ me, id }) {
  const [st, reload] = useLoad(() => api('GET', '/users'), [id]);
  return html`<${Loading} state=${st}>${(d) => {
    const row = d.items.find((x) => String(x.user.id) === String(id));
    if (!row) return html`<div class="alert bad">用户不存在。</div>`;
    return html`<${UserEdit} me=${me} row=${row} defaults=${d.defaults} reload=${reload} />`;
  }}<//>`;
}

function UserEdit({ me, row, defaults, reload }) {
  const u = row.user;
  const init = () => ({ display_name: u.display_name, role: u.role, disabled: u.disabled, note: u.note,
    quota_storage_mb: u.quota_storage_mb ?? '', quota_artifacts: u.quota_artifacts ?? '', quota_file_mb: u.quota_file_mb ?? '', quota_req_per_min: u.quota_req_per_min ?? '' });
  const [f, setF] = useState(init);
  useEffect(() => setF(init()), [u.id]);
  const [pw, setPw] = useState('');
  const [typed, setTyped] = useState('');
  const save = async (e) => {
    e.preventDefault();
    const body = { display_name: f.display_name, role: f.role, disabled: f.disabled, note: f.note, clear_quotas: [] };
    for (const k of ['quota_storage_mb', 'quota_artifacts', 'quota_file_mb', 'quota_req_per_min']) {
      if (f[k] === '' || f[k] == null) body.clear_quotas.push(k); else body[k] = +f[k];
    }
    try { await api('PATCH', '/users/' + u.id, body); toast('已保存'); reload(); } catch (err) { toast(err.message, true); }
  };
  const q = (k, label, def, unit) => html`<label class="f"><span>${label} <small>默认 ${def} ${unit}</small></span>
    <input class="i" id=${'q-' + k} type="number" min="1" placeholder=${'默认 ' + def} value=${f[k]} onInput=${(e) => setF({ ...f, [k]: e.target.value })} /></label>`;
  return html`<div class="stack">
    <div class="page-head"><div><h1>${u.name}</h1><p class="row"><span>${u.role === 'admin' ? '管理员' : '普通用户'}</span><span>创建于 ${fmtTime(u.created_at)}</span><span>${row.artifacts} 个 artifact</span><span>${fmtBytes(row.storage_bytes)}</span></p></div>
      <div class="row"><a class="btn" href=${'#/artifacts?owner=' + u.id}>查看其 artifact</a><a class="btn" href=${'#/keys?user=' + u.id}>管理其密钥</a></div></div>
    <div class="grid-2">
      <${Panel} title="资料与限额"><form class="stack" onSubmit=${save}>
        <div class="form-grid">
          <label class="f"><span>显示名</span><input class="i" id="e-display" value=${f.display_name} onInput=${(e) => setF({ ...f, display_name: e.target.value })} /></label>
          <label class="f"><span>角色</span><select class="i" id="e-role" value=${f.role} onChange=${(e) => setF({ ...f, role: e.target.value })}><option value="user">普通用户</option><option value="admin">管理员</option></select></label>
        </div>
        <label class="f"><span>备注</span><input class="i" id="e-note" value=${f.note} onInput=${(e) => setF({ ...f, note: e.target.value })} /></label>
        <div class="form-grid">
          ${q('quota_storage_mb', '存储', defaults.quota_storage_mb, 'MB')}
          ${q('quota_artifacts', 'Artifact 数', defaults.quota_artifacts, '个')}
          ${q('quota_file_mb', '单文件', defaults.quota_file_mb, 'MB')}
          ${q('quota_req_per_min', '每分钟请求', defaults.quota_req_per_min, '次')}
        </div>
        <label class="check"><input type="checkbox" id="e-disabled" checked=${f.disabled} onChange=${(e) => setF({ ...f, disabled: e.target.checked })} />停用这个用户（立即退出登录，密钥失效）</label>
        <div><button class="btn primary" type="submit">保存</button></div>
      </form><//>
      <div class="stack">
        <${Panel} title="重置密码"><form class="row" onSubmit=${async (e) => { e.preventDefault(); try { await api('POST', `/users/${u.id}/password`, { password: pw }); setPw(''); toast('密码已重置，旧会话已退出'); } catch (err) { toast(err.message, true); } }}>
          <input class="i grow" id="e-pass" type="password" autocomplete="new-password" placeholder="新密码，至少 8 位" value=${pw} onInput=${(e) => setPw(e.target.value)} required minlength="8" />
          <button class="btn" type="submit">重置</button></form><//>
        ${u.id !== me.user.id && html`<${Panel} title="删除用户">
          <p class="muted" style="margin:0 0 10px">会同时彻底删除该用户的 ${row.artifacts} 个 artifact 和全部历史版本，无法恢复。输入用户名 <span class="mono">${u.name}</span> 确认。</p>
          <div class="row"><input class="i grow" id="e-confirm" value=${typed} onInput=${(e) => setTyped(e.target.value)} placeholder=${u.name} />
            <${Confirm} label="删除用户" confirm="确认删除" disabled=${typed !== u.name} onConfirm=${async () => { await api('DELETE', '/users/' + u.id); toast('已删除用户'); go('/users'); }} /></div>
        <//>`}
      </div>
    </div>
  </div>`;
}

// ---------- 设置 ----------

function Settings() {
  const [st, reload] = useLoad(() => api('GET', '/settings'), []);
  return html`<div class="stack">
    <div class="page-head"><div><h1>设置</h1><p>全局默认值。单个用户的限额可以在用户页单独调整。</p></div></div>
    <${Loading} state=${st}>${(d) => html`<${SettingsForm} s=${d.settings} reload=${reload} />`}<//>
  </div>`;
}

function SettingsForm({ s, reload }) {
  const [f, setF] = useState({ ...s, cdn: (s.cdn_allowlist || []).join('\n') });
  const num = (k, label, unit) => html`<label class="f"><span>${label} <small>${unit}</small></span><input class="i" id=${'s-' + k} type="number" min="0" value=${f[k]} onInput=${(e) => setF({ ...f, [k]: +e.target.value })} /></label>`;
  const save = async (e) => {
    e.preventDefault();
    const body = { ...f, cdn_allowlist: f.cdn.split('\n').map((x) => x.trim()).filter(Boolean) };
    delete body.cdn;
    try { await api('PUT', '/settings', body); toast('设置已保存'); reload(); } catch (err) { toast(err.message, true); }
  };
  return html`<form class="stack" onSubmit=${save}>
    <${Panel} title="默认限额（每个用户）"><div class="form-grid">
      ${num('quota_storage_mb', '存储', 'MB')}${num('quota_artifacts', 'Artifact 数', '个')}${num('quota_file_mb', '单文件', 'MB')}
      ${num('quota_version_mb', '单个版本', 'MB')}${num('quota_files', '单个版本的文件数', '个')}
      ${num('quota_req_per_min', '每个密钥每分钟请求', '次')}${num('quota_publish_per_min', '每个用户每分钟发布', '次')}
    </div><//>
    <${Panel} title="版本与回收站"><div class="form-grid">
      ${num('max_versions', '每个 artifact 保留的版本数', '0 表示不限')}
      ${num('trash_days', '回收站保留天数', '0 表示不自动清理')}
      <label class="f"><span>新建时的默认可见性</span><select class="i" id="s-vis" value=${f.default_visibility} onChange=${(e) => setF({ ...f, default_visibility: e.target.value })}>
        <option value="private">私有</option><option value="unlisted">持链接可看</option><option value="public">公开</option></select></label>
    </div>
    <label class="check" style="margin-top:12px"><input type="checkbox" id="s-self" checked=${f.allow_self_keys} onChange=${(e) => setF({ ...f, allow_self_keys: e.target.checked })} />允许普通用户自己签发密钥</label>
    <//>
    <${Panel} title="外部资源白名单">
      <p class="muted" style="margin:0 0 8px">artifact 页面只能从本服务的 /_lib/ 和这里列出的源加载脚本、样式和字体。每行一个，以 https:// 开头。</p>
      <textarea class="i" id="s-cdn" rows="6" value=${f.cdn} onInput=${(e) => setF({ ...f, cdn: e.target.value })}></textarea>
    <//>
    <div><button class="btn primary" type="submit">保存设置</button></div>
  </form>`;
}

// ---------- 审计日志 ----------

function Audit({ route }) {
  const action = route.q.get('action') || '', user = route.q.get('user') || '', offset = +(route.q.get('offset') || 0);
  const users = useUsers(true);
  const [st] = useLoad(() => api('GET', `/audit?limit=100&offset=${offset}` + (action ? '&action=' + action : '') + (user ? '&user=' + user : '')), [action, user, offset]);
  const set = (p) => go('/audit', { action, user, ...p, offset: p.offset || '' });
  return html`<div class="stack">
    <div class="page-head"><div><h1>审计日志</h1><p>谁在什么时候做了什么。保留最近 5 万条。</p></div></div>
    <div class="row">
      <select class="i" style="width:auto" id="au-action" aria-label="操作类型" value=${action} onChange=${(e) => set({ action: e.target.value })}>
        <option value="">全部操作</option><option value="artifact.">Artifact</option><option value="key.">密钥</option><option value="user.">用户</option><option value="login">登录</option><option value="settings">设置</option><option value="gc">清理</option></select>
      <select class="i" style="width:auto" id="au-user" aria-label="用户" value=${user} onChange=${(e) => set({ user: e.target.value })}>
        <option value="">全部用户</option>${users.map((u) => html`<option value=${u.id}>${u.name}</option>`)}</select>
    </div>
    <${Loading} state=${st}>${(d) => html`<section class="panel">${d.items.length === 0 ? html`<div class="empty">没有记录。</div>` : html`
      <div class="table-wrap"><table class="t">
        <thead><tr><th>时间</th><th>用户</th><th>操作</th><th>对象</th><th>详情</th><th>IP</th></tr></thead>
        <tbody>${d.items.map((e) => html`<tr>
          <td class="nowrap muted num">${fmtTime(e.ts)}</td>
          <td class="nowrap">${e.user_name || html`<span class="faint">${e.ip === 'cli' ? '命令行' : '系统'}</span>`}${e.key_id && html` <span class="tag">密钥</span>`}</td>
          <td class="nowrap"><span class=${'tag' + (e.action === 'login.fail' ? ' bad' : e.action.endsWith('delete') || e.action.endsWith('purge') || e.action.endsWith('revoke') ? ' warn' : '')}>${ACTIONS[e.action] || e.action}</span></td>
          <td class="mono">${e.action.startsWith('artifact.') ? html`<a href=${'#/a/' + e.target}>${e.target}</a>` : e.target}</td>
          <td class="muted">${e.detail}</td>
          <td class="mono faint">${e.ip}</td></tr>`)}</tbody></table></div>`}
      <${Pager} total=${d.total} offset=${offset} limit=${100} onPage=${(o) => set({ offset: o })} /></section>`}<//>
  </div>`;
}

// ---------- 账户 ----------

function Account({ me, onChange }) {
  const [f, setF] = useState({ old: '', new: '', again: '' });
  const submit = async (e) => {
    e.preventDefault();
    if (f.new !== f.again) { toast('两次输入的新密码不一致', true); return; }
    try { const r = await api('POST', '/password', { old: f.old, new: f.new }); CSRF = r.csrf; setF({ old: '', new: '', again: '' }); toast('密码已修改，其他设备上的登录已退出'); onChange(); }
    catch (err) { toast(err.message, true); }
  };
  return html`<div class="stack">
    <div class="page-head"><div><h1>账户</h1><p>${me.user.name}${me.user.display_name ? '（' + me.user.display_name + '）' : ''}，服务地址 <span class="mono">${me.server}</span></p></div></div>
    <div class="grid-2">
      <${Panel} title="修改密码"><form class="stack" onSubmit=${submit}>
        ${me.has_password && html`<label class="f"><span>当前密码</span><input class="i" id="p-old" type="password" autocomplete="current-password" value=${f.old} onInput=${(e) => setF({ ...f, old: e.target.value })} required /></label>`}
        <label class="f"><span>新密码 <small>至少 8 位</small></span><input class="i" id="p-new" type="password" autocomplete="new-password" minlength="8" value=${f.new} onInput=${(e) => setF({ ...f, new: e.target.value })} required /></label>
        <label class="f"><span>再输入一次</span><input class="i" id="p-again" type="password" autocomplete="new-password" value=${f.again} onInput=${(e) => setF({ ...f, again: e.target.value })} required /></label>
        <div><button class="btn primary" type="submit">修改密码</button></div>
      </form><//>
      <${Panel} title="接入 agent">
        <p style="margin:0 0 10px">在<a href="#/keys">密钥页</a>签发一把密钥，页面会给出完整的接入提示词，粘贴给 agent 即可。</p>
        <p class="muted" style="margin:0">技能包地址：<a class="mono" href="/skill.tar.gz">${me.server}/skill.tar.gz</a></p>
      <//>
    </div>
  </div>`;
}

// ---------- 框架 ----------

function Shell({ me, route, refresh }) {
  const isAdmin = me.user.role === 'admin';
  const p = route.path;
  const items = [
    ['/', '概览', 'overview'],
    ['/artifacts', 'Artifact', 'artifacts'],
    ['/keys', '密钥', 'keys'],
    ...(isAdmin ? [['group', '管理'], ['/users', '用户', 'users'], ['/settings', '设置', 'settings'], ['/audit', '审计日志', 'audit']] : []),
    ['/trash', '回收站', 'trash'],
    ['/account', '账户', 'account'],
  ];
  const active = (path) => (path === '/' ? p === '/' : p === path || p.startsWith(path + '/') || (path === '/artifacts' && p.startsWith('/a/')));
  let page;
  if (p === '/') page = html`<${Overview} me=${me} />`;
  else if (p === '/artifacts') page = html`<${Artifacts} me=${me} route=${route} />`;
  else if (p.startsWith('/a/')) page = html`<${ArtifactDetail} me=${me} id=${p.slice(3)} />`;
  else if (p === '/keys') page = html`<${Keys} me=${me} route=${route} />`;
  else if (p === '/trash') page = html`<${Artifacts} me=${me} route=${route} trash=${true} />`;
  else if (p === '/account') page = html`<${Account} me=${me} onChange=${refresh} />`;
  else if (isAdmin && p === '/users') page = html`<${Users} me=${me} />`;
  else if (isAdmin && p.startsWith('/users/')) page = html`<${UserDetail} me=${me} id=${p.slice(7)} />`;
  else if (isAdmin && p === '/settings') page = html`<${Settings} />`;
  else if (isAdmin && p === '/audit') page = html`<${Audit} route=${route} />`;
  else page = html`<div class="empty">没有这个页面。<a href="#/">回到概览</a></div>`;
  const logout = async () => { try { await api('POST', '/logout'); } catch (e) { /* 忽略 */ } location.hash = '#/login'; refresh(); };
  return html`<div class="shell">
    <aside class="side">
      <a class="brand" href="/"><${Mark} />Artifact</a>
      <nav class="nav" aria-label="主导航">${items.map((it) => (it[0] === 'group' ? html`<div class="group">${it[1]}</div>` : html`<a href=${'#' + it[0]} class=${active(it[0]) ? 'on' : ''} aria-current=${active(it[0]) ? 'page' : null}><${Icon} name=${it[2]} />${it[1]}</a>`))}</nav>
      <div class="who"><span class="ellipsis">${me.user.display_name || me.user.name}</span><button class="btn sm ghost" onClick=${logout}>退出</button></div>
    </aside>
    <main class="main">${page}</main>
  </div>`;
}

function App() {
  const route = useRoute();
  const [me, setMe] = useState(null);
  const refresh = useCallback(() => {
    api('GET', '/me').then((r) => { CSRF = r.csrf || ''; setMe(r); }, () => setMe({ user: null }));
  }, []);
  useEffect(refresh, []);
  if (!me) return html`<div class="empty">正在加载…</div>`;
  if (!me.user || route.path === '/login') {
    if (me.user && route.path === '/login') { location.hash = '#/'; return null; }
    return html`<${Login} onLogin=${refresh} /><${Toast} />`;
  }
  return html`<${Shell} me=${me} route=${route} refresh=${refresh} /><${Toast} />`;
}

render(html`<${App} />`, document.getElementById('app'));
