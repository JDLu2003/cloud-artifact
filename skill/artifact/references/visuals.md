# 图表、示意图与图片

页面需要图时读。“图片丰富”的意思是：凡是图比文字更快让读者看懂的地方，都有一张合适的图。不是每节都塞一张图，更不是放装饰图。

## 1. 什么时候画，画什么

| 你要表达的 | 用 | 工具 |
|---|---|---|
| 机制：数据怎么流、组件怎么协作、请求经过哪些步骤 | 示意图、架构图、流程图 | 内联 SVG；简单流程可用 mermaid |
| 状态变化 | 状态图 | mermaid `stateDiagram-v2` 或 SVG |
| 时间顺序 | 时间线、甘特图 | SVG；mermaid `gantt` |
| 数字的趋势 | 折线图 | Chart.js 或 SVG |
| 类别之间比大小 | 条形图（类别名长时用横向） | Chart.js 或 SVG |
| 部分与整体 | 堆叠条形图；不超过 4 部分时可用环形图 | Chart.js |
| 分布 | 直方图、箱线图 | Chart.js、D3 |
| 两个变量的关系 | 散点图 | Chart.js、D3 |
| 精确数值、多属性比较 | 表格 | HTML 表格 |
| 界面、实物、现场 | 截图、照片 | 放进目录的图片文件 |
| 导航、分类提示 | 图标 | Lucide（`/_lib/lucide`） |

一句话能说清的，就不画图。

## 2. 示意图：画机制，不画名字

- 一个写着“缓存”的方框不比文字多说什么。要画出请求经过缓存的路径、缓存两边连着什么、去掉缓存后哪条箭头消失。
- 比较两个方案时，画出差异：两种架构并排，或者前后对比，读者能指着图说出“区别在这里”。各画一个孤立的方框不算比较。
- 画到决策需要的程度：一跳的问题三个框就够；涉及消息队列重排写入顺序的，就要画出队列、写入方、读取方和顺序箭头。不画整个系统的清单。
- **箭头要有标签**：`写入`、`每 30 秒轮询`、`失效通知`。没有标签的箭头只表示“有点关系”。
- 只有同一种编码（虚线、颜色）反复出现时才需要图例，否则把含义直接写在线上。

## 3. 内联 SVG 写法

```html
<figure>
  <svg viewBox="0 0 640 200" role="img" aria-label="请求先查 Redis，未命中时回源数据库并回填缓存">
    <defs>
      <marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
        <path d="M0 0L10 5L0 10z" fill="currentColor"/>
      </marker>
    </defs>
    <g fill="none" stroke="currentColor" stroke-width="1.5">
      <rect x="20" y="70" width="120" height="56" rx="6"/>
      <rect x="260" y="70" width="120" height="56" rx="6"/>
      <rect x="500" y="70" width="120" height="56" rx="6"/>
      <path d="M140 98H256" marker-end="url(#arr)"/>
      <path d="M380 98H496" marker-end="url(#arr)" style="stroke: var(--accent)"/>
    </g>
    <g fill="currentColor" font-size="14" text-anchor="middle">
      <text x="80" y="103">应用服务</text>
      <text x="320" y="103">Redis</text>
      <text x="560" y="103">数据库</text>
      <text x="198" y="88" font-size="12">查询</text>
      <text x="438" y="88" font-size="12" style="fill: var(--accent)">未命中时回源</text>
    </g>
  </svg>
  <figcaption>未命中时才访问数据库，读到的结果写回 Redis。</figcaption>
</figure>
```

要点：

- 用 `viewBox` 定尺寸，CSS 里写 `svg { max-width: 100%; height: auto; }` 让它缩放。宽的流程从左到右，分层结构从上到下。
- 线条、文字、箭头用 `currentColor`，自动跟随页面主题；只有承载含义的那一个元素用强调色变量，写在 `style` 里（`style="stroke: var(--accent)"`）。
- 中文文字宽度约等于字号乘以字数，14px 的五个字约 70px，按此留足方框宽度。文字 12–14px。
- 坐标对齐到网格，间距统一。目测摆放的图看起来就是乱的。
- 一张图说一件事。用 `<figure>` 包裹，`<figcaption>` 写出结论；`<svg>` 加 `role="img"` 和同样意思的 `aria-label`。
- SVG 内部不写 `<script>`、`<style>`、`<foreignObject>`；渐变和箭头的 id 在同一个 SVG 里定义。
- 需要大量手写路径的装饰图，说明这张图该换个做法，简化它。

## 4. mermaid

适合节点不多的流程图、时序图、状态图、甘特图。

```html
<pre class="mermaid">
flowchart LR
  A["用户提交订单"] --> B{"库存充足？"}
  B -- 是 --> C["锁定库存"]
  B -- 否 --> D["提示缺货"]
</pre>
```

- HTML 页面里写 `<pre class="mermaid">`，服务端会自动加载 mermaid；Markdown 里用 ```` ```mermaid ```` 代码块。
- 中文标签加双引号。
- 节点超过十五个时，拆成两张图，或者改用手写 SVG 控制布局。
- mermaid 的配色跟随系统明暗主题；需要精确控制样式时用 SVG。

## 5. 数据图表

用 Chart.js（`/_lib/chartjs/chart.umd.js`）：

```html
<figure>
  <div style="position:relative;height:320px"><canvas id="latency" role="img" aria-label="迁移后 P99 延迟从约 1.8 秒降到 0.3 秒以下"></canvas></div>
  <figcaption>迁移在 9 月 12 日完成，之后 P99 延迟稳定在 300 毫秒以下。数据来源：APM，按天取最大值。</figcaption>
</figure>
<script src="/_lib/chartjs/chart.umd.js"></script>
<script>
const css = getComputedStyle(document.documentElement);
const v = name => css.getPropertyValue(name).trim();
Chart.defaults.color = v('--muted');
Chart.defaults.borderColor = v('--line');
Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
new Chart(document.getElementById('latency'), {
  type: 'line',
  data: {
    labels: ['9/8', '9/9', '9/10', '9/11', '9/12', '9/13', '9/14'],
    datasets: [{ label: 'P99 延迟（毫秒）', data: [1820, 1760, 1905, 1690, 410, 280, 265],
                 borderColor: v('--accent'), backgroundColor: v('--accent'), tension: 0.2, pointRadius: 3 }]
  },
  options: { maintainAspectRatio: false, plugins: { legend: { display: false } },
             scales: { y: { beginAtZero: true, title: { display: true, text: '毫秒' } } } }
});
</script>
```

原则：

- 图表颜色、文字颜色从 CSS 变量取，明暗主题都清楚。
- 坐标轴写单位。条形图从 0 开始。不用 3D，不用双纵轴（除非确实必要且标注清楚）。
- 只有一个数据系列时去掉图例，在标题或坐标轴里说明。
- 直接在线条末端或柱子上标数值，往往比让读者对照图例和坐标轴更好。
- 用颜色突出你要说明的那个点或那条线，其余用中性色。
- 类别名较长（中文常见）时用横向条形图。
- 图表下方写数据来源和时间范围。没有真实数据时，在图注里写明“示例数据”。

简单的图（几根柱子、一条迷你折线）可以直接用 SVG 画，不必加载库。

## 6. 图片文件

- 截图、照片、生成的插图都放进 artifact 目录，例如 `img/`，用相对路径引用。
- 照片用 JPEG 或 WebP，宽度 1600px 以内；截图用 PNG 或 WebP；控制在几百 KB 以内。
- 每张图写 `alt`，说明图里有什么、要表达什么；纯装饰图写 `alt=""`。
- 不放与内容无关的配图、氛围图、库存照片。如果你有图片生成工具，只在图片能传达信息时使用（例如展示产品外观、场景示意），并在图注中说明是生成图。
- 外链图片可能失效或在国内打不开，能下载就下载下来。

## 7. 图标

- 只用于导航和分类提示，帮读者更快找到东西。不要给每个段落配图标。
- 用 Lucide：`<script src="/_lib/lucide/lucide.min.js"></script>`，写 `<i data-lucide="database"></i>`，最后调用 `lucide.createIcons()`。图标名见 lucide.dev。
- 不用 emoji 代替图标。

## 8. 表格

- 用于精确数值和多属性比较。
- 数字列右对齐，加 `font-variant-numeric: tabular-nums`，同一列保留相同的小数位。
- 表头写单位：`延迟（毫秒）`。
- 需要读者注意的格子，用底色或加粗标出，一张表一两处。
- 宽表格放在 `overflow-x: auto` 的容器里。
