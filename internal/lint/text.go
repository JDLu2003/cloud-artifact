package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// phrase 是一条“AI 腔”规则：命中即提示，具体是否要改由作者判断。
type phrase struct {
	re   *regexp.Regexp
	name string
}

func lit(words ...string) []phrase {
	out := make([]phrase, 0, len(words))
	for _, w := range words {
		out = append(out, phrase{re: regexp.MustCompile(regexp.QuoteMeta(w)), name: w})
	}
	return out
}

func pat(name, expr string) phrase {
	return phrase{re: regexp.MustCompile(expr), name: name}
}

// 空泛大词：读者读完得不到任何具体信息。
var zhBuzzwords = lit(
	"赋能", "抓手", "闭环", "底层逻辑", "顶层设计", "颗粒度", "心智", "打法", "组合拳", "护城河",
	"降本增效", "提质增效", "全链路", "全方位", "全维度", "多维度", "深层次", "一站式", "无缝",
	"极致", "硬核", "重磅", "震撼", "颠覆", "革命性", "里程碑式", "史诗级", "划时代", "前所未有",
	"天花板", "黑科技", "王炸", "炸裂", "宝藏", "干货满满", "建议收藏", "质的飞跃", "指数级",
	"不可或缺", "至关重要", "举足轻重",
)

// 套话连接：删掉后句子通常更好。
var zhFillers = append(lit(
	"值得注意的是", "值得一提的是", "需要指出的是", "不难发现", "不难看出", "由此可见", "综上所述",
	"总而言之", "总的来说", "简而言之", "众所周知", "毋庸置疑", "显而易见", "不言而喻",
	"在某种意义上", "某种程度上", "让我们一起", "接下来让我们", "一起来看看", "深入探讨",
	"深度解析", "全面解析", "一文读懂", "一文看懂", "手把手",
),
	pat("让我们……", `让我们(来|先|再|一起)?(看|聊|探索|深入|了解|回顾|拭目以待)`),
)

// 时代开场：与正文无关的宏大铺垫。
var zhOpeners = []phrase{
	pat("随着……的发展", `随着[^。，；！？\n]{1,20}的?(不断|快速|飞速|迅猛|日益|蓬勃)?(发展|进步|普及|演进)`),
	pat("在……时代/背景下", `在[^。，；！？\n]{1,15}(时代|浪潮|大背景|背景)(下|中|里)`),
	pat("在当今", `在当今|当今社会|当下这个`),
}

// 拔高与升华：结论停在信息上即可。
var zhElevation = []phrase{
	pat("不仅是……更是……", `不仅(仅)?是[^。！？\n]{1,30}(更|也|而且)是`),
	pat("这不仅……更……", `这不仅[^。！？\n]{1,30}更`),
	pat("开启……新篇章", `(开启|翻开|书写|谱写|迎来)(了)?[^。！？\n]{0,10}新(篇章|纪元|征程|时代|高度)`),
	pat("迈向……新高度", `迈(向|上)[^。！？\n]{0,10}新(高度|台阶|阶段)`),
	pat("扮演着……角色", `扮演(着|了)[^。！？\n]{1,12}角色`),
	pat("发挥着……作用", `(发挥|起到)(着|了)?[^。！？\n]{0,10}作用`),
	pat("注入活力", `注入(了)?(新的)?(活力|动力|动能|灵魂)`),
	pat("具有重要意义", `具有[^。！？\n]{0,6}(重要|深远|里程碑|划时代)(的)?意义|意义深远`),
}
var zhElevationLit = lit("未来可期", "拭目以待", "值得期待", "彰显了", "助力", "让我们共同")

// 英文常见 AI 腔。
var enPhrases = []phrase{
	pat("delve", `(?i)\bdelv(e|es|ing)\b`),
	pat("tapestry", `(?i)\btapestry\b`),
	pat("testament to", `(?i)\btestament to\b`),
	pat("in today's … world", `(?i)\bin today'?s [a-z -]{0,20}(world|landscape|era)\b`),
	pat("it's worth noting", `(?i)\bit'?s worth noting\b|\bit is worth noting\b`),
	pat("seamless(ly)", `(?i)\bseamless(ly)?\b`),
	pat("leverage", `(?i)\bleverag(e|es|ing)\b`),
	pat("unlock", `(?i)\bunlock(s|ing)? the\b`),
	pat("elevate", `(?i)\belevat(e|es|ing)\b`),
	pat("game-changer", `(?i)\bgame[- ]chang(er|ing)\b`),
	pat("revolutionize", `(?i)\brevolutioni[sz](e|es|ing)\b`),
	pat("in conclusion", `(?i)\bin conclusion\b`),
	pat("navigate the …", `(?i)\bnavigat(e|ing) the (complex|ever)`),
	pat("ever-evolving", `(?i)\bever-(evolving|changing)\b`),
	pat("not just … but", `(?i)\bnot just [^.]{1,40}\bbut\b`),
}

var (
	reZhNoSpace   = regexp.MustCompile(`\p{Han}[A-Za-z0-9]|[A-Za-z0-9]\p{Han}`)
	reZhSpace     = regexp.MustCompile(`\p{Han} [A-Za-z0-9]|[A-Za-z0-9] \p{Han}`)
	reZhHalfPunct = regexp.MustCompile(`\p{Han}[,;:!?]|[,;:!?]\p{Han}|\p{Han}\.(\s|$|\p{Han})|\p{Han}\(|\)\p{Han}`)
	reZhDupPunct  = regexp.MustCompile(`！{2,}|？{2,}|。{2,}|，{2,}|!!+|\?\?+|\p{Han}\.{3,}|\.{3,}\p{Han}|。。。`)
	reZhStraightQ = regexp.MustCompile(`"[^"\n]{0,40}\p{Han}[^"\n]{0,40}"`)
	reZhExclaim   = regexp.MustCompile(`！|\p{Han}!`)
	reZhDash      = regexp.MustCompile(`——`)
	reZhNotBut    = regexp.MustCompile(`不是[^。！？\n]{1,30}而是`)
	reZhJinxing   = regexp.MustCompile(`(进行|加以|予以)(了)?\p{Han}{2}`)
	reEmoji       = regexp.MustCompile(`[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B50}]`)
	reClause      = regexp.MustCompile(`[，。；：！？、\n,;:!?]`)
	reSentence    = regexp.MustCompile(`[。！？\n]`)
	reMetaSep     = regexp.MustCompile(`[ 　\t][/·|｜•][ 　\t]`)
	// 用文字指挥操作：“点击卡片查看详情”“向下滚动”“见下方”
	reUIGuide = regexp.MustCompile(`(点击|单击|点按|点一下|点开|轻点|轻触|双击|长按|悬停|鼠标移到|拖动|拖拽|滑动|滚动|下拉|上滑|下滑|左滑|右滑|按下|勾选)[^。；！？\n]{0,16}(查看|切换|跳转|展开|收起|进入|了解|前往|转到|浏览|筛选|打开|显示|隐藏|放大|缩小|选择|调整|播放|开始|试试|体验|探索|即可|可以|就能|来|以)|(点击|单击|点按|轻点|拖动|悬停|滚动)(这里|此处|上方|下方|左侧|右侧|上面|下面|任意)|(向|往)(下|上|左|右)(滚动|滑动|翻)|试着|试试(看)?(点|拖|切|调|改|输入)|(可以|可|你可以|您可以)(通过)?(点击|拖动|切换|滚动|使用下方|使用上方)`)
	// 功能说明里带一句“不做某事”
	reNegTail = regexp.MustCompile(`[，,；、]\s*(而|但|并|也|且|却|并且)?(不是|没有|不做|不会|不负责|不处理|不包含|不包括|不涉及|不再|不需要|不用|不能|不支持|不提供|不依赖|不改|不碰|不管|无需|无须|并非|而非)|而不是|而非|不是[^。！？\n]{1,30}而是|只[^。，\n]{1,12}不[^。，\n]{1,12}`)
	// 把制作要求写进页面
	reEcho     = regexp.MustCompile(`(采用|使用|运用|遵循|沿用|基于|保持)了?[^。，；\n]{1,12}(风格|设计语言|设计|配色|布局|排版|字体|主题|色调|版式)|(简洁|极简|克制|现代|清爽|干净|清晰|直观|优雅|精致)的?(设计|风格|界面|布局|排版|版式)|(响应式|自适应)(布局|设计)?|本(页|页面|文|文档|报告|工具|站)(已|支持|采用|使用|未使用|没有使用|不含|不使用|为|是)|按照?(你|您|用户)?的?(要求|需求|指示)|(根据|依据|应)(你|您|用户)?的?(要求|反馈|意见|需求)|(已|支持|兼容)(适配|兼容)?(手机|移动端|暗色|深色|亮色|浅色)(模式|主题|端)?|(未|不|没有)(使用|用|含|加)[^。，；\n]{0,6}(渐变|阴影|动画|动效|emoji|表情|感叹号|加粗|卡片)|已(更新|修改|调整|删除|删去|移除|优化|改为|改用|替换|重写|精简)|无(渐变|阴影|动画|动效|装饰)`)
	reReadTime = regexp.MustCompile(`(阅读|读完)[^\n]{0,6}\d+\s*分钟|约\s*\d+\s*分钟(读完|阅读)?$`)
)

type hit struct {
	name  string
	count int
}

func countPhrases(text string, ps []phrase) []hit {
	var out []hit
	for _, p := range ps {
		if n := len(p.re.FindAllStringIndex(text, -1)); n > 0 {
			out = append(out, hit{p.name, n})
		}
	}
	return out
}

func hitsExamples(hs []hit) ([]string, int) {
	sort.Slice(hs, func(i, j int) bool { return hs[i].count > hs[j].count })
	total := 0
	ex := []string{}
	for _, h := range hs {
		total += h.count
		if len(ex) < 8 {
			if h.count > 1 {
				ex = append(ex, fmt.Sprintf("%s ×%d", h.name, h.count))
			} else {
				ex = append(ex, h.name)
			}
		}
	}
	return ex, total
}

func contexts(text string, re *regexp.Regexp, n int) []string {
	var out []string
	for _, loc := range re.FindAllStringIndex(text, n) {
		start, end := loc[0], loc[1]
		// 向两边各扩展约 8 个字
		s := start
		for i := 0; i < 8 && s > 0; i++ {
			s--
			for s > 0 && !utf8Start(text[s]) {
				s--
			}
		}
		e := end
		for i := 0; i < 8 && e < len(text); i++ {
			e++
			for e < len(text) && !utf8Start(text[e]) {
				e++
			}
		}
		out = append(out, strings.ReplaceAll(strings.TrimSpace(text[s:e]), "\n", " "))
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// checkText 对可见文字做语言检查。strong 是加粗元素的数量。
func checkText(file, text string, strong int) []Warning {
	var ws []Warning
	han := hanCount(text)
	if han >= 20 {
		ws = append(ws, checkZh(file, text, han)...)
	}
	words := len(strings.Fields(text))
	if han < words { // 以英文为主
		if hs := countPhrases(text, enPhrases); len(hs) > 0 {
			ex, total := hitsExamples(hs)
			ws = append(ws, Warning{Level: "warn", Rule: "en-ai-phrase", File: file, Count: total,
				Message: "出现常见的 AI 腔英文措辞。换成具体、平实的说法。", Examples: ex})
		}
	}
	size := han + words
	if strong > 0 && size > 0 {
		limit := size/180 + 4
		if strong > limit {
			ws = append(ws, Warning{Level: "warn", Rule: "bold-density", File: file, Count: strong,
				Message: fmt.Sprintf("加粗过多（%d 处）。加粗只留给读者扫读时必须看到的少数词，全文控制在 %d 处以内。", strong, limit)})
		}
	}
	var meta []string
	var negTail, quantLines []string
	nonEmpty := 0
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		nonEmpty++
		// 只看单句文案：一行之内最多一个句号，长度有限
		if n := runeLen(l); n >= 4 && n <= 60 && hanCount(l) > 0 && strings.Count(l, "。") <= 1 {
			if reNegTail.MatchString(l) {
				negTail = append(negTail, l)
			}
			if reQuant.MatchString(l) {
				quantLines = append(quantLines, l)
			}
		}
		if runeLen(l) > 40 || hanCount(l) == 0 || strings.ContainsAny(l, "。；，：") {
			continue
		}
		// 分隔符拼接的元信息只在页面开头（标题附近）检查，正文里的“A / B”多半是比例或并列
		if nonEmpty > 12 && !reReadTime.MatchString(l) {
			continue
		}
		if reReadTime.MatchString(l) {
			meta = append(meta, l)
			continue
		}
		if reMetaSep.MatchString(l) {
			// 每一段都含中文才算元信息串，避免误报“TCP / TLS / HTTP”这类并列术语
			all := true
			for _, seg := range reMetaSep.Split(l, -1) {
				if hanCount(seg) == 0 {
					all = false
				}
			}
			if all {
				meta = append(meta, l)
			}
		}
	}
	if ex := contexts(text, reUIGuide, 6); len(ex) > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "ui-instruction", File: file, Count: len(reUIGuide.FindAllStringIndex(text, -1)),
			Message: "用文字指挥读者操作（“点击某处查看某物”“向下滚动”“可以通过……切换”）。这类句子默认删掉，并把控件改到不需要它也看得出怎么用。只有在教程里讲解别的软件的操作步骤时才保留。", Examples: ex})
	}
	if ex := contexts(text, reEcho, 6); len(ex) > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "process-echo", File: file, Count: len(reEcho.FindAllStringIndex(text, -1)),
			Message: "疑似把制作要求、设计说明或修改记录写进了页面（风格声明、“按要求”“已适配手机”“已改为”）。读者没有看过对话，这类句子默认删掉，要说就写在给用户的回复里。只有当它讲的是页面内容本身（例如被介绍的产品采用某种设计）才保留。", Examples: ex})
	}
	if len(negTail) > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "negative-tail", File: file, Count: len(negTail),
			Message: "单句文案里带了“不是 / 没有 / 不做某事”。没做的事列不完，说明功能只写它做到了什么，默认删掉否定的那一半。只有读者照着操作会有损失的警告才保留，并放在操作发生的地方。", Examples: firstN(negTail, 5)})
	}
	if len(quantLines) > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "empty-quantifier", File: file, Count: len(quantLines),
			Message: "单句文案里有数量词（“一个”“一次”“同一个”“第一次”“一次性”）。绝大多数是凑出来的具体感，默认删掉数量词或重写这句话。只有换一个数句子的意思就变了，才保留。", Examples: firstN(quantLines, 5)})
	}
	if len(meta) > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "meta-string", File: file, Count: len(meta),
			Message: "用 / · | 拼接的元信息串、面包屑小标签或“约 N 分钟”阅读时间，是模板化的装饰。作者、日期确实需要时分开写；其余删掉。", Examples: firstN(meta, 3)})
	}
	if n := len(reEmoji.FindAllString(text, -1)); n > 3 {
		ws = append(ws, Warning{Level: "warn", Rule: "emoji", File: file, Count: n,
			Message: "正文里 emoji 较多。不要用 emoji 充当图标、项目符号或情绪渲染。"})
	}
	return ws
}

func checkZh(file, text string, han int) []Warning {
	var ws []Warning
	if n := len(reZhNoSpace.FindAllStringIndex(text, -1)); n > 0 {
		spaced := len(reZhSpace.FindAllStringIndex(text, -1))
		level := "warn"
		msg := "中文与英文、数字之间缺少空格（本服务约定加一个半角空格，例：“部署到 3 台服务器上”“使用 GitHub 登录”）。"
		if spaced > 0 {
			msg = fmt.Sprintf("中英文间距不一致：%d 处有空格，%d 处没有。统一加一个半角空格。", spaced, n)
		}
		ws = append(ws, Warning{Level: level, Rule: "zh-spacing", File: file, Count: n, Message: msg, Examples: contexts(text, reZhNoSpace, 4)})
	}
	if n := len(reZhHalfPunct.FindAllStringIndex(text, -1)); n > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "zh-punctuation", File: file, Count: n,
			Message: "中文句子里用了半角标点。中文语境用全角：，。；：！？（）", Examples: contexts(text, reZhHalfPunct, 4)})
	}
	if n := len(reZhDupPunct.FindAllStringIndex(text, -1)); n > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "zh-repeated-punct", File: file, Count: n,
			Message: "重复标点或用英文句点代替省略号。不叠用标点；省略号写作“……”。", Examples: contexts(text, reZhDupPunct, 3)})
	}
	if n := len(reZhStraightQ.FindAllStringIndex(text, -1)); n > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "zh-quotes", File: file, Count: n,
			Message: "中文内容用了直引号 \"…\"。改用弯引号 “…”，引号内再引用用 ‘…’。", Examples: contexts(text, reZhStraightQ, 3)})
	}
	if n := len(reZhExclaim.FindAllStringIndex(text, -1)); n > 2 {
		ws = append(ws, Warning{Level: "warn", Rule: "zh-exclamation", File: file, Count: n,
			Message: fmt.Sprintf("感叹号 %d 个。说明性文字基本不需要感叹号，用内容本身说服读者。", n)})
	}
	if n := len(reZhDash.FindAllStringIndex(text, -1)); n > 0 && n*1000/han >= 3 && n >= 3 {
		ws = append(ws, Warning{Level: "warn", Rule: "zh-dash", File: file, Count: n,
			Message: fmt.Sprintf("破折号偏多（每千字 %s 个）。插入语改成独立短句，解释改用冒号或括号。", pct(n, han)), Examples: contexts(text, reZhDash, 3)})
	}
	if n := len(reZhNotBut.FindAllStringIndex(text, -1)); n >= 2 {
		ws = append(ws, Warning{Level: "warn", Rule: "zh-not-but", File: file, Count: n,
			Message: "“不是……而是……”句式用了多次。直接说是什么，不必先立一个靶子。", Examples: contexts(text, reZhNotBut, 3)})
	}
	if n := len(reZhJinxing.FindAllStringIndex(text, -1)); n >= 3 {
		ws = append(ws, Warning{Level: "info", Rule: "zh-jinxing", File: file, Count: n,
			Message: "“进行/加以/予以 + 动词”偏多。直接用动词：“进行了优化”→“优化了”。", Examples: contexts(text, reZhJinxing, 3)})
	}
	if hs := countPhrases(text, zhBuzzwords); len(hs) > 0 {
		ex, total := hitsExamples(hs)
		ws = append(ws, Warning{Level: "warn", Rule: "zh-buzzword", File: file, Count: total,
			Message: "空泛大词或夸张词。换成具体的事实、数字或动作；“大幅提升”不如“从 3.2 秒降到 0.8 秒”。", Examples: ex})
	}
	if hs := countPhrases(text, zhFillers); len(hs) > 0 {
		ex, total := hitsExamples(hs)
		ws = append(ws, Warning{Level: "warn", Rule: "zh-filler", File: file, Count: total,
			Message: "套话连接词。删掉后多数句子更直接；需要转折或总结时用最朴素的词。", Examples: ex})
	}
	if hs := countPhrases(text, zhOpeners); len(hs) > 0 {
		ex, total := hitsExamples(hs)
		ws = append(ws, Warning{Level: "warn", Rule: "zh-grand-opener", File: file, Count: total,
			Message: "宏大的时代式开场。开头直接说读者要知道的事。", Examples: ex})
	}
	if hs := append(countPhrases(text, zhElevation), countPhrases(text, zhElevationLit)...); len(hs) > 0 {
		ex, total := hitsExamples(hs)
		ws = append(ws, Warning{Level: "warn", Rule: "zh-elevation", File: file, Count: total,
			Message: "拔高或升华的句式。结尾停在结论、数据或下一步行动上，不替读者总结意义。", Examples: ex})
	}
	// 一个分句里“的”字过多，通常是定语堆叠
	var deChains []string
	for _, c := range reClause.Split(text, -1) {
		if strings.Count(c, "的") >= 4 && runeLen(c) <= 80 {
			deChains = append(deChains, strings.TrimSpace(c))
		}
	}
	if len(deChains) > 0 {
		ws = append(ws, Warning{Level: "info", Rule: "zh-de-chain", File: file, Count: len(deChains),
			Message: "单个分句里“的”字堆叠（4 个及以上），读起来拗口。拆句或改用动词结构。", Examples: firstN(deChains, 3)})
	}
	var long []string
	for _, s := range reSentence.Split(text, -1) {
		if hanCount(s) > 110 {
			long = append(long, short(strings.TrimSpace(s), 40))
		}
	}
	if len(long) > 0 {
		ws = append(ws, Warning{Level: "info", Rule: "zh-long-sentence", File: file, Count: len(long),
			Message: "有超过 110 字的长句。一句话只讲一件事，拆成两三句。", Examples: firstN(long, 3)})
	}
	return ws
}
