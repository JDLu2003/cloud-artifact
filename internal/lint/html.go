// Package lint 对 artifact 做发布前检查：页面规范与中文/英文文字规范。
// 检查只给出告警，不阻止发布。
package lint

import (
	"bytes"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlDoc 是一次 HTML 扫描收集到的信息。
type htmlDoc struct {
	Title        string
	HasViewport  bool
	HasHTMLTag   bool
	Scripts      []string // 外链脚本 src
	Styles       []string // 外链样式 href
	Refs         []string // 所有相对/绝对资源引用
	ImgNoAlt     int
	ImgNoAltEx   []string
	Text         string // 可见文字（不含 code/pre/script/style）
	StrongCount  int
	HeadingTexts []string
}

var inlineTags = map[string]bool{
	"a": true, "b": true, "strong": true, "em": true, "i": true, "span": true, "small": true, "sub": true,
	"sup": true, "mark": true, "abbr": true, "time": true, "u": true, "s": true, "q": true, "cite": true,
	"label": true, "del": true, "ins": true, "dfn": true, "var": true, "bdi": true, "bdo": true, "data": true,
	"wbr": true, "tspan": true, "font": true,
}

var skipTextTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Textarea: true,
	atom.Code: true, atom.Pre: true, atom.Kbd: true, atom.Samp: true, atom.Title: true,
}

var refAttrs = map[string][]string{
	"img":    {"src"},
	"script": {"src"},
	"link":   {"href"},
	"source": {"src"},
	"video":  {"src", "poster"},
	"audio":  {"src"},
	"iframe": {"src"},
	"a":      {"href"},
	"image":  {"href", "xlink:href"},
	"embed":  {"src"},
	"object": {"data"},
	"track":  {"src"},
}

func attr(t html.Token, name string) (string, bool) {
	for _, a := range t.Attr {
		key := a.Key
		if a.Namespace != "" {
			key = a.Namespace + ":" + a.Key
		}
		if strings.EqualFold(key, name) {
			return a.Val, true
		}
	}
	return "", false
}

func scanHTML(src []byte) *htmlDoc {
	d := &htmlDoc{}
	z := html.NewTokenizer(bytes.NewReader(src))
	var text strings.Builder
	skip := 0
	inTitle := false
	heading := 0
	var headBuf strings.Builder
	prevEnd := false // 上一个记号是结束标签：紧挨着的兄弟元素通常由布局分开，不算连在一起的文字
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		t := z.Token()
		wasEnd := prevEnd
		prevEnd = tt == html.EndTagToken
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			if wasEnd {
				text.WriteByte('\n')
			}
			a := t.DataAtom
			switch a {
			case atom.Html:
				d.HasHTMLTag = true
			case atom.Title:
				if d.Title == "" {
					inTitle = true
				}
			case atom.Meta:
				if v, _ := attr(t, "name"); strings.EqualFold(v, "viewport") {
					d.HasViewport = true
				}
			case atom.Script:
				if v, ok := attr(t, "src"); ok {
					d.Scripts = append(d.Scripts, strings.TrimSpace(v))
				}
			case atom.Link:
				rel, _ := attr(t, "rel")
				if v, ok := attr(t, "href"); ok && strings.Contains(strings.ToLower(rel), "stylesheet") {
					d.Styles = append(d.Styles, strings.TrimSpace(v))
				}
			case atom.Img:
				if _, ok := attr(t, "alt"); !ok {
					d.ImgNoAlt++
					if src, _ := attr(t, "src"); len(d.ImgNoAltEx) < 3 {
						d.ImgNoAltEx = append(d.ImgNoAltEx, short(src, 60))
					}
				}
			case atom.Strong, atom.B:
				d.StrongCount++
			case atom.H1, atom.H2, atom.H3, atom.H4:
				heading++
				headBuf.Reset()
			}
			for _, name := range refAttrs[t.Data] {
				if v, ok := attr(t, name); ok && strings.TrimSpace(v) != "" {
					// <link rel=preconnect> 之类不是资源引用
					if a == atom.Link {
						rel, _ := attr(t, "rel")
						rel = strings.ToLower(rel)
						if strings.Contains(rel, "preconnect") || strings.Contains(rel, "dns-prefetch") || strings.Contains(rel, "canonical") {
							continue
						}
					}
					d.Refs = append(d.Refs, strings.TrimSpace(v))
				}
			}
			if v, ok := attr(t, "srcset"); ok {
				for _, part := range strings.Split(v, ",") {
					f := strings.Fields(strings.TrimSpace(part))
					if len(f) > 0 {
						d.Refs = append(d.Refs, f[0])
					}
				}
			}
			if tt == html.StartTagToken && skipTextTags[a] {
				if a == atom.Title {
					// title 文本单独收集
				} else {
					skip++
				}
			}
			if !inlineTags[t.Data] {
				text.WriteByte('\n')
			}
		case html.EndTagToken:
			a := t.DataAtom
			if a == atom.Title {
				inTitle = false
			} else if skipTextTags[a] && skip > 0 {
				skip--
			}
			if a == atom.H1 || a == atom.H2 || a == atom.H3 || a == atom.H4 {
				if heading > 0 {
					heading--
					d.HeadingTexts = append(d.HeadingTexts, strings.TrimSpace(headBuf.String()))
				}
			}
			if !inlineTags[t.Data] {
				text.WriteByte('\n')
			}
		case html.TextToken:
			if inTitle {
				d.Title += t.Data
				continue
			}
			if skip == 0 {
				text.WriteString(t.Data)
				if heading > 0 {
					headBuf.WriteString(t.Data)
				}
			}
		}
	}
	d.Title = strings.TrimSpace(collapseSpace(d.Title))
	d.Text = text.String()
	return d
}

var spaceRe = regexp.MustCompile(`[ \t\r\n\f]+`)

func collapseSpace(s string) string { return spaceRe.ReplaceAllString(s, " ") }

// VisibleText 返回 HTML 中读者能看到的文字（用于检索和检查）。
func VisibleText(src []byte) string {
	d := scanHTML(src)
	lines := strings.Split(d.Text, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(collapseSpace(l))
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// HTMLTitle 返回 <title> 的内容。
func HTMLTitle(src []byte) string {
	if len(src) > 64*1024 {
		src = src[:64*1024]
	}
	return scanHTML(src).Title
}

// HasHTMLSkeleton 判断页面是否自带 <html> 骨架。
func HasHTMLSkeleton(src []byte) bool {
	head := src
	if len(head) > 4096 {
		head = head[:4096]
	}
	l := bytes.ToLower(head)
	return bytes.Contains(l, []byte("<html")) || bytes.Contains(l, []byte("<!doctype"))
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// resolveRef 把页面中的引用解析为包内路径。返回 kind：
// "skip"（外部链接、锚点等，不检查）、"local"（包内相对路径）、"root"（以 / 开头的站内路径）、"external"。
func resolveRef(fromFile, ref string) (kind, target string) {
	r := strings.TrimSpace(ref)
	lr := strings.ToLower(r)
	switch {
	case r == "" || strings.HasPrefix(r, "#"):
		return "skip", ""
	case strings.HasPrefix(lr, "data:"), strings.HasPrefix(lr, "blob:"), strings.HasPrefix(lr, "mailto:"),
		strings.HasPrefix(lr, "tel:"), strings.HasPrefix(lr, "sms:"), strings.HasPrefix(lr, "javascript:"),
		strings.HasPrefix(lr, "about:"):
		return "skip", ""
	case strings.HasPrefix(lr, "http://"), strings.HasPrefix(lr, "https://"), strings.HasPrefix(lr, "//"):
		return "external", r
	case strings.Contains(r, "{{") || strings.Contains(r, "${"):
		return "skip", "" // 模板占位，交给运行时
	}
	if i := strings.IndexAny(r, "?#"); i >= 0 {
		r = r[:i]
	}
	if u, err := url.PathUnescape(r); err == nil {
		r = u
	}
	if strings.HasPrefix(r, "/") {
		return "root", path.Clean(r)
	}
	if r == "" {
		return "skip", ""
	}
	return "local", strings.TrimPrefix(path.Clean(path.Join(path.Dir(fromFile), r)), "./")
}

func originOf(raw string) string {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}
