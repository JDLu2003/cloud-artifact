---
name: artifact
description: 把网页、文档、报告、幻灯片、图表、可视化和小工具发布到自托管的 Artifact 服务，返回浏览器可打开的链接，并管理版本（更新、局部修改、对比、回滚）。用户说“做成网页/页面/报告/文档/幻灯片/PPT/图表/仪表盘”“发个链接给我”“publish an artifact / make a page / share it as a link”，或要修改之前发布过的页面时使用。
---

# Artifact

把内容做成一个页面，发布到 {{SERVER_URL}}，把链接交给用户。服务端保存每一个版本，可以对比和回滚。

下文的 `art` 指 `python3 <本技能目录>/scripts/artifact.py`。

## 开始之前

运行 `art whoami`。

- 输出用户名和配额：可以开始。
- 提示“缺少配置”：请用户提供 API Key（在服务的管理后台签发），然后运行 `art setup --server <地址> --key <Key>`。
- 提示“技能有新版本”：运行 `art self-update`，再重新读本文件。

## 判断是否该做成 artifact

适合：用户会在浏览器里看、会转发、会反复打开或操作的成品，例如报告、技术文档、教程、幻灯片、数据图表、交互小工具、带图的讲解。

不适合：几句话就能答完的问题；只给开发者看的代码改动；用户要的是文件本身（.docx、.pptx、.md），这时直接给文件。

拿不准就问用户一句，不要默认发布。

## 工作流

1. **定下三件事**：这是给谁看的，读者看完要知道或能做什么，形式是什么（文档、幻灯片、图表页、工具）。用户没说清楚时，用一句话提出你的理解，再开始。
2. **只读需要的参考**。按下表挑，不必全读：

   | 要做的 | 先读 |
   |---|---|
   | 任何 HTML 页面 | `references/page-contract.md`（运行环境的硬规则，必读） |
   | 文章、报告、技术文档、教程 | `references/documents.md` |
   | 幻灯片、演示稿 | `references/slides.md`，并从 `assets/templates/deck.html` 起步 |
   | 图表、示意图、流程图、配图 | `references/visuals.md` |
   | 版面规划、配色、字体、版式 | `references/design.md` |
   | 中文内容 | `references/writing-zh.md`（中文内容必读） |
   | 任何文字（标题、正文、按钮） | `references/writing.md` |
   | 发布前自查 | `references/review.md` |
   | 不用脚本、直接调接口 | `references/api.md` |

3. **规划版面**。列出这一页要承担的每项任务，逐项决定它的呈现方式，再决定各部分怎么放在一起（`design.md` 第 2 节）。除了以阅读为主的文章，不要把所有内容排成一条从上到下的长页。
4. **在本地写文件**。一个 artifact 对应一个目录，入口是 `index.html`（也可以是 `index.md`，服务端用内置阅读主题渲染）。图片、数据、样式放在同一目录，用相对路径引用。模板在 `assets/templates/`：`document.html`（长文）、`deck.html`（幻灯片）、`dashboard.html`（数据页）。
5. **检查**：`art check <目录>`。“错误”必须修；“警告”逐条判断，确实是问题就改，其中 `heading-quantifier`、`empty-quantifier`、`negative-tail`、`process-echo`、`ui-instruction` 五条命中的句子大多该删，默认删掉或重写；“建议”按需处理。
6. **自查**：按 `references/review.md` 过一遍，重点看事实、逻辑和语言。能截图的环境，看一眼桌面和手机宽度的效果，改一轮，不要反复循环。
7. **发布**：`art publish <目录> -m "这次做了什么"`。输出里有链接和检查结果。
8. **回复用户**：给链接，用一两句话说明做了什么、有什么需要用户确认的。不要把整页内容再贴一遍。

## 修改已经发布的 artifact

- **同一个目录再次 publish**，会更新同一个链接，生成新版本。脚本记住了目录和 artifact 的对应关系。
- **小改动**（改一句话、换一个数字、修一个样式）：
  `art edit <ID> --old "原文" --new "新文" -m "说明"`。原文必须在文件里恰好出现一次，多行文本用 `--old-file/--new-file`。
- **改别的会话做的 artifact**：`art list -q 关键词` 找到 ID，`art pull <ID> <目录>` 取回源码，改完 `art publish <目录>`。
- **版本冲突（409）**：说明别处已经更新过。先 `art pull` 合并，再发布；只有确认要覆盖时才加 `--force`。
- **看历史、对比、回滚**：`art versions <ID>`、`art diff <ID> [--from N --to M]`、`art restore <ID> <版本>`。回滚会生成新版本，不会删除历史。
- **改标题或可见性**：`art set <ID> --title ... --visibility private|unlisted|public`。
- **删除**：只在用户明确要求时执行 `art delete <ID> --yes`。

## 硬规则（细节见 page-contract.md）

- 页面运行在沙箱里，与服务器不同源：`localStorage` 可能不可用，读写要包在 `try/catch` 里；没有后端，表单只能在页面内处理。
- 外部脚本只能来自本服务的 `/_lib/`（推荐，国内也能访问）或白名单 CDN。`art libs` 列出内置库。
- 第一行写 `<title>`，用 2–12 个字给页面起一个具体的名字。
- 颜色写成 CSS 变量，同时提供亮色和暗色两套。
- 手机宽度（约 400px）下可用，页面不能横向滚动。

## 内容要求（细节见 writing.md、writing-zh.md）

- 写给人读：先给结论，再给依据；用具体的事实、数字和例子，不用空泛的形容词。
- 图要传达信息：示意图画机制，图表画数据，每张图配一句说明它在说什么。不放装饰性的图。
- 语气克制：不夸张，不喊口号，不在结尾升华。
- 标题和单句文案里的数量必须是信息。`一份配置描述一个服务`、`一次完整的过程` 这类标题，数量词删掉以后内容不变，就不该这样写；`同一个`、`第一次`、`一次性` 同样要查。
- 说明一个部分的功能时只写它做到了什么，后面不拖一句“不做某事”。
- 用户提的要求和禁忌照做即可，不写进页面。页面上不出现 `采用某某风格`、`按要求改为表格` 这类讲制作过程的话，它们属于给用户的回复。
- 界面靠控件引导操作，不写 `点击某处查看某物`。
- 中文遵守排版规范：中英文之间加空格，用全角标点，不滥用加粗、破折号和感叹号。

## 命令速查

```
art whoami                              身份、配额、技能版本
art check <目录>                        检查，不保存
art publish <目录> -m 说明              发布或更新
art publish <目录> --new                强制新建一个 artifact
art edit <ID> --old 原文 --new 新文     局部修改
art list [-q 关键词]                    列出
art info <ID>                           详情与文件
art versions <ID>                       版本历史
art diff <ID> [--from N --to M]         对比
art restore <ID> <版本>                 回滚（生成新版本）
art pull <ID> <目录>                    取回源码
art set <ID> --visibility private       改可见性：private 私有 / unlisted 持链接可看 / public 公开
art libs                                内置前端库清单
art self-update                         更新本技能
```
