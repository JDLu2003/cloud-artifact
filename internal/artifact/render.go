package artifact

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"

	"vps-artifact/internal/lint"
)

var (
	reMdTitle    = regexp.MustCompile(`(?m)^#\s+(.+?)\s*#*\s*$`)
	reFrontTitle = regexp.MustCompile(`(?m)^title:\s*["']?(.+?)["']?\s*$`)
	reFrontDesc  = regexp.MustCompile(`(?m)^description:\s*["']?(.+?)["']?\s*$`)
	reFrontBlock = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n`)
	reMetaDesc   = regexp.MustCompile(`(?is)<meta\s+[^>]*name=["']description["'][^>]*content=["']([^"']+)["']`)
	reMetaDesc2  = regexp.MustCompile(`(?is)<meta\s+[^>]*content=["']([^"']+)["'][^>]*name=["']description["']`)
)

// FrontMatter 解析 Markdown 开头 --- 包裹的简单键值（只支持 title、description）。
func FrontMatter(src []byte) (title, desc string, body []byte) {
	m := reFrontBlock.FindSubmatchIndex(src)
	if m == nil {
		return "", "", src
	}
	block := src[m[2]:m[3]]
	if t := reFrontTitle.FindSubmatch(block); t != nil {
		title = string(t[1])
	}
	if d := reFrontDesc.FindSubmatch(block); d != nil {
		desc = string(d[1])
	}
	return title, desc, src[m[1]:]
}

func extractTitle(kind string, src []byte) string {
	if len(src) == 0 {
		return ""
	}
	switch kind {
	case "html", "svg":
		return lint.HTMLTitle(src)
	case "markdown":
		t, _, body := FrontMatter(src)
		if t != "" {
			return t
		}
		if m := reMdTitle.FindSubmatch(body); m != nil {
			return strings.TrimSpace(string(m[1]))
		}
	}
	return ""
}

func extractDescription(kind string, src []byte) string {
	switch kind {
	case "html":
		head := src
		if len(head) > 16384 {
			head = head[:16384]
		}
		if m := reMetaDesc.FindSubmatch(head); m != nil {
			return string(m[1])
		}
		if m := reMetaDesc2.FindSubmatch(head); m != nil {
			return string(m[1])
		}
	case "markdown":
		_, d, _ := FrontMatter(src)
		return d
	}
	return ""
}

func searchText(kind string, src []byte) string {
	var t string
	switch kind {
	case "html", "svg":
		t = lint.VisibleText(src)
	default:
		t = string(src)
	}
	if len(t) > 200000 {
		t = t[:200000]
	}
	return t
}

// Skeleton 是没有 <html> 的页面外层骨架，与 Claude Artifact 的约定一致：
// 只放 charset、viewport 和极少的 reset，样式由页面自己定义。
const skeletonHead = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<style>
:root{color-scheme:light;padding-top:env(safe-area-inset-top,0px);padding-bottom:env(safe-area-inset-bottom,0px)}
body{margin:0;font:16px/1.6 system-ui,-apple-system,"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Noto Sans CJK SC","Source Han Sans SC",sans-serif}
img{max-width:100%}
[hidden]{display:none!important}
</style>
`

var (
	reMermaidUse    = regexp.MustCompile(`class=["'][^"']*\bmermaid\b`)
	reMermaidScript = regexp.MustCompile(`(?i)<script[^>]+mermaid`)
	reBodyClose     = regexp.MustCompile(`(?i)</body\s*>`)
)

const mermaidLoader = `<script src="/_lib/mermaid/mermaid.min.js"></script>
<script>(function(){try{var d=matchMedia('(prefers-color-scheme: dark)').matches;var t=document.documentElement.getAttribute('data-theme');if(t)d=t==='dark';mermaid.initialize({startOnLoad:true,theme:d?'dark':'neutral',securityLevel:'strict',fontFamily:'inherit'});}catch(e){}})();</script>
`

// WrapHTML 给缺少骨架的页面补上骨架，并在需要时注入 mermaid。
func WrapHTML(src []byte) []byte {
	needMermaid := reMermaidUse.Match(src) && !reMermaidScript.Match(src)
	var out bytes.Buffer
	if !lint.HasHTMLSkeleton(src) {
		out.WriteString(skeletonHead)
		// 页面内容里的 <title>、<style>、<meta> 放在 body 开头也能生效，浏览器会正确处理
		out.WriteString("</head>\n<body>\n")
		out.Write(src)
		if needMermaid {
			out.WriteString(mermaidLoader)
		}
		out.WriteString("\n</body>\n</html>\n")
		return out.Bytes()
	}
	if !needMermaid {
		return src
	}
	if loc := reBodyClose.FindIndex(src); loc != nil {
		out.Write(src[:loc[0]])
		out.WriteString(mermaidLoader)
		out.Write(src[loc[0]:])
		return out.Bytes()
	}
	out.Write(src)
	out.WriteString(mermaidLoader)
	return out.Bytes()
}

// slugify 生成标题锚点，保留中文。
func slugify(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
