// Package skillpack 把内置的技能目录打包成 tar.gz，供 agent 下载安装。
package skillpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const Name = "artifact"

type Pack struct {
	fsys    fs.FS // 以技能目录为根
	Version string
	files   []string
	built   time.Time

	mu    sync.Mutex
	cache map[string][]byte
}

// New 读取技能目录，计算内容版本号。
func New(fsys fs.FS) (*Pack, error) {
	p := &Pack{fsys: fsys, cache: map[string][]byte{}, built: time.Now().Truncate(time.Second)}
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		p.files = append(p.files, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(p.files)
	for _, f := range p.files {
		b, _ := fs.ReadFile(fsys, f)
		h.Write([]byte(f))
		h.Write(b)
	}
	p.Version = hex.EncodeToString(h.Sum(nil))[:10]
	return p, nil
}

func (p *Pack) Files() []string { return p.files }

func isText(name string) bool {
	switch path.Ext(name) {
	case ".md", ".py", ".txt", ".html", ".css", ".js", ".json", ".sh", ".svg":
		return true
	}
	return false
}

// File 返回替换过占位符的单个文件内容。
func (p *Pack) File(name, serverURL string) ([]byte, bool) {
	b, err := fs.ReadFile(p.fsys, name)
	if err != nil {
		return nil, false
	}
	if isText(name) {
		b = p.render(b, serverURL)
	}
	return b, true
}

func (p *Pack) render(b []byte, serverURL string) []byte {
	s := string(b)
	s = strings.ReplaceAll(s, "{{SERVER_URL}}", serverURL)
	s = strings.ReplaceAll(s, "{{SKILL_VERSION}}", p.Version)
	return []byte(s)
}

// Tarball 返回 artifact/ 目录的 tar.gz。
func (p *Pack) Tarball(serverURL string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.cache[serverURL]; ok {
		return b, nil
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	mt := p.built
	dirs := map[string]bool{}
	for _, f := range p.files {
		dir := path.Dir(f)
		var parts []string
		for d := dir; d != "." && d != "/"; d = path.Dir(d) {
			parts = append([]string{d}, parts...)
		}
		for _, d := range append([]string{"."}, parts...) {
			full := path.Join(Name, d) + "/"
			if !dirs[full] {
				dirs[full] = true
				tw.WriteHeader(&tar.Header{Name: full, Mode: 0o755, Typeflag: tar.TypeDir, ModTime: mt})
			}
		}
		data, _ := p.File(f, serverURL)
		mode := int64(0o644)
		if strings.HasPrefix(f, "scripts/") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: path.Join(Name, f), Mode: mode, Size: int64(len(data)), ModTime: mt, Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	p.cache[serverURL] = buf.Bytes()
	return buf.Bytes(), nil
}

// Prompt 生成交给 agent 的接入提示词。key 为空时用占位符。
func Prompt(serverURL, key, userName string) string {
	if key == "" {
		key = "<在管理后台为你签发的 API Key>"
	}
	r := strings.NewReplacer("{URL}", serverURL, "{KEY}", key, "{USER}", userName)
	return r.Replace(promptTemplate)
}

const promptTemplate = `请帮我接入 Artifact 服务。接入后，你可以把网页、文档、报告、幻灯片和数据可视化发布成链接，我在浏览器里打开查看。

服务地址：{URL}
API Key：{KEY}
这个 Key 只用于第 3 步，不要写进技能目录或代码仓库，也不要在回复里重复它。

请按顺序完成：

1. 确定你的技能目录：pi 用 ~/.pi/agent/skills，Claude Code 用 ~/.claude/skills，Codex 用 ~/.codex/skills；其他 agent 按自己的文档确定。如果你没有技能机制，就用 ~/.artifact-skill，以后每次需要时先读其中的 SKILL.md。

2. 下载并解压技能，完成后会有 <技能目录>/artifact/SKILL.md：
   mkdir -p <技能目录> && (curl -fsSL {URL}/skill.tar.gz || curl -fsSL --noproxy '*' {URL}/skill.tar.gz) | tar -xz -C <技能目录>
   第二个 curl 是在代理无法访问该地址时改为直连。

3. 保存凭据并验证（凭据写入 ~/.config/artifact/config.json，权限 600）：
   python3 <技能目录>/artifact/scripts/artifact.py setup --server {URL} --key {KEY}
   成功时会输出用户名、配额和用量。

4. 读一遍 <技能目录>/artifact/SKILL.md。以后我请你做网页、文档、报告、幻灯片或图表时，按其中的流程写好、检查、发布，然后把链接发给我。

完成后用一两句话告诉我：技能装在哪里，验证输出了什么。
`
