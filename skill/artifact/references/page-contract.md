# 页面运行环境（硬规则）

写任何 HTML 页面前读一遍。这些规则由服务端的沙箱和安全策略强制执行，违反了不会报错，只会让页面在用户那里静悄悄地坏掉。

## 目录

1. 文件与入口
2. 骨架
3. 沙箱：能做什么，不能做什么
4. 外部资源与内置库
5. 标题与描述
6. 响应式
7. 明暗主题
8. 大小限制
9. Markdown 入口

## 1. 文件与入口

- 一个 artifact 是一个目录。入口文件按顺序查找：`index.html`、`index.md`、`README.md`；目录里只有一个 `.html` 时用它。也可以用 `--entry` 指定。
- 其他文件（图片、CSS、JS、JSON 数据、字体）和入口放在一起，用**相对路径**引用：`<img src="img/arch.png">`、`fetch('data/sales.json')`。
- 不要用以 `/` 开头的路径引用自己的文件，那会指向服务器根目录。唯一的例外是内置库 `/_lib/...`。
- 文件名只用字母、数字、`-`、`_`、`.` 和中文，不要用空格、`#`、`?`、`%`。
- 多页面站点可以有多个 `.html`，用相对链接互相跳转。

## 2. 骨架

可以写完整的 `<!doctype html><html>…`，也可以省略。省略时服务端会补上：

```html
<!doctype html><html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<style>
:root{color-scheme:light;padding-top:env(safe-area-inset-top,0px);padding-bottom:env(safe-area-inset-bottom,0px)}
body{margin:0;font:16px/1.6 system-ui,-apple-system,"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Noto Sans CJK SC",sans-serif}
img{max-width:100%}
[hidden]{display:none!important}
</style>
</head><body>  …你的内容…  </body></html>
```

省略骨架时，把 `<title>` 和 `<style>` 放在文件最前面。自己写完整骨架时，必须带 viewport 那一行。

## 3. 沙箱：能做什么，不能做什么

页面在一个与服务器**不同源**的沙箱里运行（相当于 iframe 的 `sandbox` 不含 `allow-same-origin`）。由此：

| 能做 | 不能做或不可靠 |
|---|---|
| 运行内联和本服务 `/_lib` 的脚本 | 读写 cookie |
| `fetch()` 包内的相对路径文件（JSON、CSV、图片） | `fetch()` 白名单以外的网站 |
| 打开新窗口、外部链接（`target="_blank"`） | 把表单提交到任何地址（`form-action 'none'`） |
| `<a download>` 下载页面生成的文件 | 用 iframe 嵌入其他网站（YouTube、地图等），改为放链接 |
| 剪贴板写入（放在点击事件里，失败时退回到选中文本） | 摄像头、麦克风、定位等设备权限 |
| Canvas、WebGL；Web Worker 只能用 `blob:` 地址创建 | Service Worker；用包内 `.js` 文件直接 `new Worker()` |
| `alert/confirm` 可用，但更好的做法是在页面里做确认 UI | |

**localStorage 和 sessionStorage 在沙箱里会抛出异常。** 如果要用，读写都包在 `try/catch` 里，并保证没有存储时页面照常显示：

```js
function load(key, fallback) {
  try { const v = localStorage.getItem(key); return v ? JSON.parse(v) : fallback; }
  catch (e) { return fallback; }
}
```

这类存储只适合“记住上次选的标签页”这种可有可无的偏好。需要多人共享或可靠保存的数据，这个服务目前不提供，直接告诉用户。

页面没有后端。表单可以收集输入并在页面内计算、展示，但处理 `submit` 时要 `event.preventDefault()`，不要把 `action` 指向任何地址。

## 4. 外部资源与内置库

**首选内置库。** 服务器在 `/_lib/` 下提供固定版本的常用库，用户在国内也能稳定加载：

| 库 | 路径 | 用法 |
|---|---|---|
| Chart.js 4.4.7 | `/_lib/chartjs/chart.umd.js` | 全局 `Chart` |
| D3 7.9.0 | `/_lib/d3/d3.min.js` | 全局 `d3` |
| Mermaid 11.4.1 | `/_lib/mermaid/mermaid.min.js` | 页面里有 `<pre class="mermaid">` 时服务端自动注入，无需手动引入 |
| KaTeX 0.16.11 | `/_lib/katex/katex.min.js`、`katex.min.css`、`auto-render.min.js` | 数学公式 |
| highlight.js 11.10.0 | `/_lib/highlight/highlight.min.js`，样式 `styles/github.min.css`、`styles/github-dark.min.css` | 代码高亮 |
| Lucide 0.460.0 | `/_lib/lucide/lucide.min.js` | 图标：`<i data-lucide="file-text"></i>` 后调用 `lucide.createIcons()` |
| Preact + htm | `/_lib/preact/htm-preact.module.js` | `import { html, render, useState } from '/_lib/preact/htm-preact.module.js'`，写交互组件不需要构建 |

`art libs` 可以随时列出完整清单。

**白名单 CDN**（`art whoami` 显示当前白名单）：默认允许 `cdnjs.cloudflare.com`、`cdn.jsdelivr.net`、`unpkg.com` 的脚本和样式，以及 Google Fonts。使用时：

- 版本号写死，例如 `https://cdnjs.cloudflare.com/ajax/libs/three.js/0.160.0/three.min.js`，不要用 `@latest` 或不带版本的地址。
- 选 UMD 或带全局变量的构建，`<script src>` 放在使用它的内联脚本之前。
- 这些 CDN 在中国大陆可能很慢或打不开。读者可能在国内时，只用 `/_lib`。

**其他来源的脚本和样式会被浏览器拦截**，不会有任何错误提示。`art check` 会报 `blocked-script` / `blocked-style`。

**图片**可以引用任意 https 地址，但外链会失效、会变慢。能下载的图片就放进目录里。

**字体**：中文页面用系统字体栈，不要加载中文网络字体（一个字体动辄数 MB）。推荐：

```css
--sans: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", "Source Han Sans SC", sans-serif;
--serif: "Songti SC", "STSong", "Noto Serif CJK SC", "Source Han Serif SC", "SimSun", serif;
--mono: ui-monospace, SFMono-Regular, "JetBrains Mono", Menlo, Consolas, monospace;
```

只有英文标题需要特殊字体时，才从 Google Fonts 加载西文字体，并写好回退字体。

## 5. 标题与描述

- `<title>` 是 artifact 在列表、浏览器标签和查看页顶栏里的名字。写一个名字，不写一句话：2–12 个字的名词短语，具体到能和其他页面区分开。
  - 好：`2026 年第三季度销售复盘`、`Redis 缓存迁移方案`、`新人入职手册`
  - 不好：`报告`、`数据分析`、`销售复盘：深度解析第三季度的增长与挑战`
- 用户已经给这件事起了名字，就用用户的名字。
- 一句话说明放在 `<meta name="description" content="…">`，或发布时用 `--description`，会显示在列表里。
- 更新时保持标题稳定，除非内容的主题变了。

## 6. 响应式

页面必须在约 400px 宽的手机上可用，页面主体**不能横向滚动**。

- 两侧留白至少 16px，写在 `body` 或最外层容器上：
  `padding-left: max(16px, env(safe-area-inset-left, 0px)); padding-right: max(16px, env(safe-area-inset-right, 0px));`
  垂直方向用 `padding-block`，不要用会把左右清零的 `padding` 简写。
- 用 flex 或 grid 的 `gap` 排列同级元素；窄屏时换行或改成单列。
- 放长文本、代码、表格的 flex/grid 子元素加 `min-width: 0`。
- 图片和带 `aspect-ratio` 的盒子加 `max-width: 100%`。
- 宽表格、宽图、代码块各自放进 `overflow-x: auto` 的容器里，只让它们自己横向滚动。
- 首屏高度跟着内容走，不要用 `height: 100vh` 撑满一屏。全屏应用（游戏、白板）用 `html, body { height: 100% }`。
- 数字对齐的地方加 `font-variant-numeric: tabular-nums`。

## 7. 明暗主题

读者的系统可能是暗色。所有颜色写成 CSS 变量，亮色值写在 `:root`，暗色值在下面两个块里重新定义：

```css
:root { --bg: #fafaf9; --fg: #1f2329; --muted: #646a73; --line: #e3e5e8; --accent: #2f5dd1; }
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) { --bg: #17191d; --fg: #e4e6ea; --muted: #a0a6b1; --line: #2d3138; --accent: #8fb0ff; color-scheme: dark; }
}
:root[data-theme="dark"] { --bg: #17191d; --fg: #e4e6ea; --muted: #a0a6b1; --line: #2d3138; --accent: #8fb0ff; color-scheme: dark; }
body { background: var(--bg); color: var(--fg); }
```

- 每个变量都要先在 `:root` 里定义；只在暗色块里出现的颜色，在亮色下就没有值。
- `body` 一定要设置背景色，不能透明。
- 组件只用变量，不写只在一种主题下好看的固定颜色。图表的文字、坐标轴、网格线也从变量取色。
- 暗色不是简单反相：重新检查对比度，强调色在两种背景上都要清楚。
- 确实只适合一种风格的页面（比如复古终端），可以只做一套，但要明确写死背景色和所有文字颜色。

## 8. 大小限制

`art whoami` 会显示你的限额。默认单文件 10 MB、单个版本 30 MB、每个版本最多 300 个文件。

- 照片用 JPEG 或 WebP，宽度 1600px 以内通常就够。
- 不要把大图转成 base64 内嵌在 HTML 里，放成单独文件。
- 大数据集先在本地聚合好，只发布页面真正要画的部分。

## 9. Markdown 入口

入口是 `.md` 时，服务端用内置的中文阅读主题渲染，适合以文字为主的文档：

- 支持 GFM（表格、任务列表、删除线、自动链接）、脚注、定义列表。
- 开头可以写 front matter：
  ```
  ---
  title: Redis 缓存迁移方案
  description: 把会话缓存从单机 Redis 迁到集群的步骤与回滚预案
  ---
  ```
- 代码块自动高亮；```` ```mermaid ```` 代码块会画成图；```` ```math ```` 代码块用 KaTeX 渲染。
- 二级、三级标题有 3 个以上时，宽屏会自动显示目录。
- 图片用相对路径：`![系统架构：请求经网关分发到三个服务](img/arch.png)`。
- 需要复杂版式、交互或精细控制视觉时，改用 HTML。
