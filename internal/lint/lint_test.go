package lint

import (
	"strings"
	"testing"
)

func rules(ws []Warning) map[string]Warning {
	m := map[string]Warning{}
	for _, w := range ws {
		m[w.Rule] = w
	}
	return m
}

func run(html string, paths ...string) map[string]Warning {
	all := map[string]bool{"index.html": true}
	for _, p := range paths {
		all[p] = true
	}
	return rules(Check(Input{
		Files:     map[string][]byte{"index.html": []byte(html)},
		AllPaths:  all,
		Entry:     "index.html",
		Allowlist: []string{"https://cdnjs.cloudflare.com"},
		LibExists: func(p string) bool { return p == "chartjs/chart.umd.js" },
	}))
}

func TestHTMLRules(t *testing.T) {
	r := run(`<p>hi</p><img src="img/a.png"><script src="https://evil.example/x.js"></script><script src="/_lib/chartjs/chart.umd.js"></script><script src="/_lib/nope.js"></script>`)
	for _, want := range []string{"missing-title", "missing-file", "img-alt", "blocked-script", "bad-root-path"} {
		if _, ok := r[want]; !ok {
			t.Errorf("expected rule %s, got %v", want, keys(r))
		}
	}
	if w := r["bad-root-path"]; len(w.Examples) != 1 || w.Examples[0] != "/_lib/nope.js" {
		t.Errorf("only the missing lib should be reported: %+v", w)
	}
	r = run(`<title>好</title><img src="img/a.png" alt="图"><script src="https://cdnjs.cloudflare.com/ajax/libs/x/1.0/x.js"></script><style>@media (prefers-color-scheme: dark){}</style>`, "img/a.png")
	if len(r) != 0 {
		t.Errorf("clean page should have no warnings, got %v", keys(r))
	}
	r = run(`<!-- TEMPLATE-SAMPLE --><title>x</title>`)
	if r["template-sample"].Level != "error" {
		t.Error("template marker should be an error")
	}
}

func TestChineseRules(t *testing.T) {
	text := `<title>报告</title><style>:root{color-scheme:light}</style>
<p>随着人工智能技术的飞速发展，我们对系统进行了全方位的优化,取得了质的飞跃！！值得注意的是，这不仅是一次升级，更是一次革新。</p>
<p>我们使用GitHub登录，部署到3台服务器上，用户说"非常好用"。</p>`
	r := run(text)
	for _, want := range []string{"zh-grand-opener", "zh-buzzword", "zh-filler", "zh-elevation", "zh-punctuation", "zh-repeated-punct", "zh-spacing", "zh-quotes"} {
		if _, ok := r[want]; !ok {
			t.Errorf("expected rule %s, got %v", want, keys(r))
		}
	}
	clean := `<title>迁移复盘</title><style>:root{color-scheme:light}</style>
<p>第三季度，我们把会话缓存从单机 Redis 迁到了三节点集群。迁移后，高峰期登录接口的 P99 延迟从 1.8 秒降到 240 毫秒。</p>
<p>还有两件事需要在 10 月底前决定：是否给报表服务接入新集群，以及告警阈值是否收紧到 300 毫秒。</p>`
	if r := run(clean); len(r) != 0 {
		t.Errorf("clean Chinese text should pass, got %v", keys(r))
	}
}

func TestAdjacentElementsNotJoined(t *testing.T) {
	r := run(`<title>页</title><style>:root{color-scheme:light}</style><div><span>林栩</span><span>2026 年 10 月 16 日</span></div><p>这是一段足够长的中文内容，用来触发中文检查规则，确保相邻元素不会误报。</p>`)
	if _, ok := r["zh-spacing"]; ok {
		t.Errorf("sibling elements should not be treated as adjacent text: %+v", r["zh-spacing"])
	}
}

func TestTemplateTells(t *testing.T) {
	r := run(`<title>页</title><style>:root{color-scheme:light}</style><p>工程笔记 / HTTP 传输机制</p><p>面向后端工程师　/　约 8 分钟</p>
<h1>HTTP/3：把限制拆开</h1><h2>队头阻塞：从连接缩小到流</h2><h2>连接建立：合并两次等待</h2><h2>连接迁移：地址变了身份不变</h2>
<p>这是一段足够长的中文内容，用来触发中文检查规则，确保规则能正常运行。</p>`)
	for _, want := range []string{"heading-colon", "meta-string"} {
		if _, ok := r[want]; !ok {
			t.Errorf("expected rule %s, got %v", want, keys(r))
		}
	}
}

func TestMarkdown(t *testing.T) {
	ws := Check(Input{
		Files:    map[string][]byte{"index.md": []byte("# 标题\n\n![](img/x.png)\n\n**一** **二** **三** **四** **五** **六** 这是正文这是正文这是正文这是正文这是正文。\n\n```\n随着技术的飞速发展\n```\n")},
		AllPaths: map[string]bool{"index.md": true},
		Entry:    "index.md",
	})
	r := rules(ws)
	if _, ok := r["missing-file"]; !ok {
		t.Error("markdown image should be checked")
	}
	if _, ok := r["img-alt"]; !ok {
		t.Error("markdown image without alt should be reported")
	}
	if _, ok := r["zh-grand-opener"]; ok {
		t.Error("text inside code fences must be ignored")
	}
	if _, ok := r["bold-density"]; !ok {
		t.Error("too much bold should be reported")
	}
}

func keys(m map[string]Warning) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ",")
}
