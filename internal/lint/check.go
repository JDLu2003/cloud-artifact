package lint

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Warning struct {
	Level    string   `json:"level"` // error：几乎肯定是问题；warn：大概率要改；info：建议
	Rule     string   `json:"rule"`
	File     string   `json:"file,omitempty"`
	Message  string   `json:"message"`
	Count    int      `json:"count,omitempty"`
	Examples []string `json:"examples,omitempty"`
}

type Input struct {
	Files     map[string][]byte // 需要检查的文本文件（html/md/css/js/svg）
	AllPaths  map[string]bool   // 包内全部文件路径
	Entry     string
	Allowlist []string          // 允许的外部脚本/样式源，如 https://cdnjs.cloudflare.com
	LibExists func(string) bool // 判断 /_lib/ 下的路径是否存在
}

// Check 运行全部规则。结果按严重程度排序。
func Check(in Input) []Warning {
	var ws []Warning
	names := make([]string, 0, len(in.Files))
	for p := range in.Files {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		src := in.Files[p]
		ext := strings.ToLower(path.Ext(p))
		switch ext {
		case ".html", ".htm":
			ws = append(ws, checkHTML(p, src, in)...)
		case ".md", ".markdown":
			ws = append(ws, checkMarkdown(p, src, in)...)
		case ".svg":
			if p == in.Entry {
				ws = append(ws, checkText(p, VisibleText(src), 0)...)
			}
		}
	}
	rank := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(ws, func(i, j int) bool { return rank[ws[i].Level] < rank[ws[j].Level] })
	return ws
}

func Summary(ws []Warning) (errors, warns, infos int) {
	for _, w := range ws {
		switch w.Level {
		case "error":
			errors++
		case "warn":
			warns++
		default:
			infos++
		}
	}
	return
}

func allowed(origin string, list []string) bool {
	for _, a := range list {
		a = strings.TrimRight(strings.ToLower(strings.TrimSpace(a)), "/")
		if a == "" {
			continue
		}
		if origin == a || strings.HasPrefix(origin+"/", a+"/") {
			return true
		}
	}
	return false
}

var (
	reVH          = regexp.MustCompile(`(?i)(min-)?height\s*:\s*100s?vh`)
	reDarkMode    = regexp.MustCompile(`prefers-color-scheme|data-theme`)
	reLorem       = regexp.MustCompile(`(?i)lorem ipsum`)
	reColorScheme = regexp.MustCompile(`color-scheme\s*:`)
	reLocalStor   = regexp.MustCompile(`\blocalStorage\b|\bsessionStorage\b`)
	reTry         = regexp.MustCompile(`\btry\s*\{`)
	reHTTPOnly    = regexp.MustCompile(`(?i)^http://`)
)

func checkHTML(file string, src []byte, in Input) []Warning {
	var ws []Warning
	d := scanHTML(src)
	isEntry := file == in.Entry
	if isEntry && d.Title == "" {
		ws = append(ws, Warning{Level: "warn", Rule: "missing-title", File: file,
			Message: "页面没有 <title>。标题会显示在浏览器标签和列表里，写一个 2–12 字的名称。"})
	}
	if d.HasHTMLTag && !d.HasViewport {
		ws = append(ws, Warning{Level: "warn", Rule: "missing-viewport", File: file,
			Message: `页面自带 <html> 骨架但缺少 <meta name="viewport" content="width=device-width, initial-scale=1">，手机上会缩成一团。`})
	}
	for _, s := range d.Scripts {
		if k, _ := resolveRef(file, s); k == "external" {
			if reHTTPOnly.MatchString(s) {
				ws = append(ws, Warning{Level: "error", Rule: "insecure-script", File: file, Message: "外链脚本用了 http://，请改为 https:// 或使用 /_lib 内置库。", Examples: []string{short(s, 90)}})
			} else if !allowed(originOf(s), in.Allowlist) {
				ws = append(ws, Warning{Level: "error", Rule: "blocked-script", File: file, Message: "脚本来源不在白名单内，会被浏览器拦截。改用 /_lib 内置库或白名单 CDN。", Examples: []string{short(s, 90)}})
			}
		}
	}
	for _, s := range d.Styles {
		if k, _ := resolveRef(file, s); k == "external" && !allowed(originOf(s), in.Allowlist) {
			ws = append(ws, Warning{Level: "error", Rule: "blocked-style", File: file, Message: "外链样式表来源不在白名单内，会被拦截。把 CSS 写进页面或放进包里。", Examples: []string{short(s, 90)}})
		}
	}
	ws = append(ws, checkRefs(file, d.Refs, in)...)
	if d.ImgNoAlt > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "img-alt", File: file, Count: d.ImgNoAlt,
			Message: "图片缺少 alt。写清图片表达了什么；纯装饰图写 alt=\"\"。", Examples: d.ImgNoAltEx})
	}
	if bytes.Contains(src, []byte("TEMPLATE-SAMPLE")) {
		ws = append(ws, Warning{Level: "error", Rule: "template-sample", File: file,
			Message: "页面还带着模板的 TEMPLATE-SAMPLE 标记。确认示例内容（标题、数字、图表数据、示意图）都换成了真实内容，然后删掉这行注释。"})
	}
	if isEntry {
		if reLorem.Match(src) {
			ws = append(ws, Warning{Level: "error", Rule: "placeholder", File: file, Message: "页面里有 lorem ipsum 占位文字，换成真实内容。"})
		}
		if reVH.Match(src) {
			ws = append(ws, Warning{Level: "info", Rule: "viewport-height", File: file,
				Message: "用了 100vh 高度。首屏通常应按内容定高；全屏应用请用 html,body{height:100%}。"})
		}
		if !reDarkMode.Match(src) && !reColorScheme.Match(src) {
			ws = append(ws, Warning{Level: "info", Rule: "no-dark-mode", File: file,
				Message: "没有暗色主题。用颜色 token + prefers-color-scheme 提供暗色，或明确只做单一主题并写死背景色。"})
		}
		if reLocalStor.Match(src) && !reTry.Match(src) {
			ws = append(ws, Warning{Level: "warn", Rule: "storage-unguarded", File: file,
				Message: "页面在沙箱中运行，访问 localStorage 会抛异常。读写都要包在 try/catch 里，并保证没有存储时页面照常显示。"})
		}
	}
	if hasBigDataURI(src, 1400000) {
		ws = append(ws, Warning{Level: "warn", Rule: "large-inline-data", File: file,
			Message: "页面内嵌了超过 1MB 的 base64 数据。把图片作为单独文件放进包里，用相对路径引用。"})
	}
	if len(d.HeadingTexts) > 0 {
		ws = append(ws, checkHeadings(file, d.HeadingTexts)...)
	}
	ws = append(ws, checkText(file, d.Text, d.StrongCount)...)
	return ws
}

func checkRefs(file string, refs []string, in Input) []Warning {
	var missing, rootMissing []string
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		k, t := resolveRef(file, r)
		switch k {
		case "local":
			if strings.HasPrefix(t, "../") || t == ".." {
				missing = append(missing, r)
				continue
			}
			if !in.AllPaths[t] && !in.AllPaths[strings.TrimSuffix(t, "/")+"/index.html"] {
				missing = append(missing, r)
			}
		case "root":
			if strings.HasPrefix(t, "/_lib/") {
				if in.LibExists != nil && !in.LibExists(strings.TrimPrefix(t, "/_lib/")) {
					rootMissing = append(rootMissing, r)
				}
			} else if !strings.HasPrefix(t, "/a/") && !strings.HasPrefix(t, "/c/") {
				rootMissing = append(rootMissing, r)
			}
		}
	}
	var ws []Warning
	if len(missing) > 0 {
		ws = append(ws, Warning{Level: "error", Rule: "missing-file", File: file, Count: len(missing),
			Message: "引用了包里不存在的文件。把文件一起发布，或修正相对路径。", Examples: firstN(missing, 5)})
	}
	if len(rootMissing) > 0 {
		ws = append(ws, Warning{Level: "error", Rule: "bad-root-path", File: file, Count: len(rootMissing),
			Message: "以 / 开头的路径指向服务根目录，不是你的包。包内文件请用相对路径；内置库请核对 /_lib 下的实际路径。", Examples: firstN(rootMissing, 5)})
	}
	return ws
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		s = s[:n]
	}
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = short(v, 90)
	}
	return out
}

var reEmojiStart = regexp.MustCompile(`^[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B50}\x{2705}\x{274C}]`)

var reHeadColon = regexp.MustCompile(`\p{Han}[^：:]{0,20}[：]|[A-Za-z0-9/]{1,20}[：]`)

func checkHeadings(file string, hs []string) []Warning {
	var ws []Warning
	var emoji, colon []string
	for _, h := range hs {
		h = strings.TrimSpace(h)
		if reEmojiStart.MatchString(h) {
			emoji = append(emoji, h)
		}
		if reHeadColon.MatchString(h) && !strings.HasSuffix(h, "：") {
			colon = append(colon, h)
		}
	}
	if len(emoji) > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "emoji-heading", File: file, Count: len(emoji),
			Message: "标题以 emoji 开头。emoji 不承担信息，去掉；需要图标就用真正的图标或图示。", Examples: firstN(emoji, 3)})
	}
	if len(colon) >= 3 && len(colon)*2 >= len(hs) {
		ws = append(ws, Warning{Level: "warn", Rule: "heading-colon", File: file, Count: len(colon),
			Message: fmt.Sprintf("%d 个标题里有 %d 个是“话题：结论”的冒号句式，读起来像套模板。结论式标题直接写成一句话，主标题不加冒号副标题。", len(hs), len(colon)), Examples: firstN(colon, 4)})
	}
	return ws
}

// ---- Markdown ----

var (
	reFence     = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	reInlineCd  = regexp.MustCompile("`[^`\n]+`")
	reMdImage   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	reMdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	reMdBold    = regexp.MustCompile(`\*\*[^*\n]+\*\*|__[^_\n]+__`)
	reMdHeading = regexp.MustCompile(`(?m)^#{1,4}\s+(.+)$`)
	reHTMLTag   = regexp.MustCompile(`<[^>]+>`)
	reFrontMat  = regexp.MustCompile(`(?s)\A---\n.*?\n---\n`)
)

func checkMarkdown(file string, src []byte, in Input) []Warning {
	var ws []Warning
	s := string(src)
	s = reFrontMat.ReplaceAllString(s, "")
	body := reFence.ReplaceAllString(s, "\n")
	body = reInlineCd.ReplaceAllString(body, " ")
	var refs []string
	noAlt := 0
	for _, m := range reMdImage.FindAllStringSubmatch(body, -1) {
		refs = append(refs, m[2])
		if strings.TrimSpace(m[1]) == "" {
			noAlt++
		}
	}
	for _, m := range reMdLink.FindAllStringSubmatch(body, -1) {
		refs = append(refs, m[2])
	}
	ws = append(ws, checkRefs(file, refs, in)...)
	if noAlt > 0 {
		ws = append(ws, Warning{Level: "warn", Rule: "img-alt", File: file, Count: noAlt, Message: "图片缺少说明文字 ![说明](路径)。"})
	}
	if strings.Contains(s, "TEMPLATE-SAMPLE") {
		ws = append(ws, Warning{Level: "error", Rule: "template-sample", File: file, Message: "还带着模板的 TEMPLATE-SAMPLE 标记，替换示例内容后删掉它。"})
	}
	if reLorem.MatchString(body) {
		ws = append(ws, Warning{Level: "error", Rule: "placeholder", File: file, Message: "有 lorem ipsum 占位文字。"})
	}
	var heads []string
	for _, m := range reMdHeading.FindAllStringSubmatch(body, -1) {
		heads = append(heads, m[1])
	}
	ws = append(ws, checkHeadings(file, heads)...)
	bold := len(reMdBold.FindAllString(body, -1))
	text := reMdImage.ReplaceAllString(body, "$1")
	text = reMdLink.ReplaceAllString(text, "$1")
	text = reHTMLTag.ReplaceAllString(text, "")
	text = strings.NewReplacer("**", "", "__", "", "#", "", "> ", "", "|", "\n").Replace(text)
	ws = append(ws, checkText(file, text, bold)...)
	return ws
}

func pct(n, d int) string {
	if d == 0 {
		return "0"
	}
	return fmt.Sprintf("%.1f", float64(n)*1000/float64(d))
}

func hanCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF || r >= 0x3400 && r <= 0x4DBF {
			n++
		}
	}
	return n
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// hasBigDataURI 判断是否有长度超过 limit 的 base64 data URI。
func hasBigDataURI(src []byte, limit int) bool {
	s := string(src)
	for {
		i := strings.Index(s, ";base64,")
		if i < 0 {
			return false
		}
		s = s[i+8:]
		n := 0
		for n < len(s) {
			c := s[n]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
				n++
				continue
			}
			break
		}
		if n >= limit {
			return true
		}
		s = s[n:]
	}
}
