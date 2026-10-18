package artifact

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

type TOCItem struct {
	Level int
	ID    string
	Text  string
}

type MarkdownDoc struct {
	Title       string
	Description string
	Body        []byte // 渲染好的 HTML 正文
	TOC         []TOCItem
	HasMermaid  bool
	HasMath     bool
	HasCode     bool
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote, extension.DefinitionList, extension.CJK),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()), // 允许内嵌 HTML（内容本身运行在沙箱里）
)

type headingIDs struct{ used map[string]int }

func (h *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := slugify(string(value))
	if base == "" {
		base = "section"
	}
	id := base
	if n := h.used[base]; n > 0 {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	h.used[base]++
	return []byte(id)
}

func (h *headingIDs) Put(value []byte) { h.used[string(value)]++ }

func nodeText(n ast.Node, src []byte) string {
	var b strings.Builder
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		case *ast.CodeSpan:
			for cc := t.FirstChild(); cc != nil; cc = cc.NextSibling() {
				if tx, ok := cc.(*ast.Text); ok {
					b.Write(tx.Segment.Value(src))
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

var (
	reMermaidBlock = regexp.MustCompile(`(?s)<pre><code class="language-mermaid">(.*?)</code></pre>`)
	reMathBlock    = regexp.MustCompile(`(?s)<pre><code class="language-(?:math|latex|katex)">(.*?)</code></pre>`)
	reDisplayMath  = regexp.MustCompile(`\$\$[^$]+\$\$`)
)

// RenderMarkdown 把 Markdown 渲染成 HTML 正文，并提取标题与目录。
func RenderMarkdown(src []byte) (*MarkdownDoc, error) {
	title, desc, body := FrontMatter(src)
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{used: map[string]int{}}))
	doc := md.Parser().Parse(text.NewReader(body), parser.WithContext(ctx))
	out := &MarkdownDoc{Title: title, Description: desc}
	firstH1 := true
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch h := n.(type) {
		case *ast.Heading:
			t := nodeText(h, body)
			if h.Level == 1 && firstH1 {
				firstH1 = false
				if out.Title == "" {
					out.Title = t
				}
			}
			if h.Level == 2 || h.Level == 3 {
				id, _ := h.AttributeString("id")
				ids, _ := id.([]byte)
				out.TOC = append(out.TOC, TOCItem{Level: h.Level, ID: string(ids), Text: t})
			}
		case *ast.FencedCodeBlock:
			out.HasCode = true
		}
		return ast.WalkContinue, nil
	})
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, body, doc); err != nil {
		return nil, err
	}
	html := buf.Bytes()
	if reMermaidBlock.Match(html) {
		out.HasMermaid = true
		html = reMermaidBlock.ReplaceAll(html, []byte(`<pre class="mermaid">$1</pre>`))
	}
	if reMathBlock.Match(html) {
		out.HasMath = true
		html = reMathBlock.ReplaceAll(html, []byte(`<div class="math-display">$1</div>`))
	}
	if reDisplayMath.Match(body) {
		out.HasMath = true
	}
	out.Body = html
	return out, nil
}
