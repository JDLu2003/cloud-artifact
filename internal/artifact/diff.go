package artifact

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"

	"vps-artifact/internal/store"
)

// DiffRow 是并排对比中的一行。Kind: eq 相同、del 删除、ins 新增、chg 修改、skip 省略的相同段落。
type DiffRow struct {
	Kind string `json:"kind"`
	LN   int    `json:"ln,omitempty"`
	RN   int    `json:"rn,omitempty"`
	L    string `json:"l,omitempty"`
	R    string `json:"r,omitempty"`
	Skip int    `json:"skip,omitempty"`
	// 修改行的行内差异：按词元切分后对比，Changed 为 true 的片段是真正改动的部分
	LSeg   []Seg `json:"-"`
	RSeg   []Seg `json:"-"`
	Inline bool  `json:"-"`
}

type Seg struct {
	Text    string
	Changed bool
}

var reToken = regexp.MustCompile(`[\p{L}\p{N}_]+|\s+|.`)

// inlineDiff 对两行做词元级对比。行太长或差异太大时返回 false，退回整行标色。
func inlineDiff(a, b string) ([]Seg, []Seg, bool) {
	if len(a) > 20000 || len(b) > 20000 {
		return nil, nil, false
	}
	ta, tb := reToken.FindAllString(a, -1), reToken.FindAllString(b, -1)
	m := difflib.NewMatcher(ta, tb)
	var la, lb []Seg
	same := 0
	push := func(segs []Seg, text string, changed bool) []Seg {
		if text == "" {
			return segs
		}
		if n := len(segs); n > 0 && segs[n-1].Changed == changed {
			segs[n-1].Text += text
			return segs
		}
		return append(segs, Seg{text, changed})
	}
	for _, op := range m.GetOpCodes() {
		x, y := strings.Join(ta[op.I1:op.I2], ""), strings.Join(tb[op.J1:op.J2], "")
		if op.Tag == 'e' {
			same += len(x)
			la, lb = push(la, x, false), push(lb, y, false)
		} else {
			la, lb = push(la, x, true), push(lb, y, true)
		}
	}
	// 相同部分不到一半时，行内高亮反而更乱
	if same*2 < max(len(a), len(b)) {
		return nil, nil, false
	}
	return la, lb, true
}

type FileDiff struct {
	Path    string    `json:"path"`
	Status  string    `json:"status"` // added | removed | modified | unchanged
	OldSize int64     `json:"old_size"`
	NewSize int64     `json:"new_size"`
	Binary  bool      `json:"binary"`
	TooBig  bool      `json:"too_big,omitempty"`
	Added   int       `json:"added"`
	Removed int       `json:"removed"`
	Unified string    `json:"unified,omitempty"`
	Rows    []DiffRow `json:"rows,omitempty"`
}

type Diff struct {
	From    int        `json:"from"`
	To      int        `json:"to"`
	Files   []FileDiff `json:"files"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
	Changed int        `json:"changed_files"`
}

const maxDiffLines = 6000

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return difflib.SplitLines(s)
}

// DiffVersions 对比同一 artifact 的两个版本。withRows 为 true 时附带并排视图需要的行数据。
func (s *Service) DiffVersions(artifactID string, from, to int, withRows bool) (*Diff, error) {
	if _, err := s.Store.VersionByNumber(artifactID, from); errors.Is(err, store.ErrNotFound) {
		return nil, errf(404, "not_found", "版本 v%d 不存在", from)
	}
	if _, err := s.Store.VersionByNumber(artifactID, to); errors.Is(err, store.ErrNotFound) {
		return nil, errf(404, "not_found", "版本 v%d 不存在", to)
	}
	a, err := s.Store.VersionFiles(artifactID, from)
	if err != nil {
		return nil, err
	}
	b, err := s.Store.VersionFiles(artifactID, to)
	if err != nil {
		return nil, err
	}
	am := map[string]store.VersionFile{}
	bm := map[string]store.VersionFile{}
	paths := map[string]bool{}
	for _, f := range a {
		am[f.Path] = f
		paths[f.Path] = true
	}
	for _, f := range b {
		bm[f.Path] = f
		paths[f.Path] = true
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	d := &Diff{From: from, To: to, Files: []FileDiff{}}
	for _, p := range names {
		fa, okA := am[p]
		fb, okB := bm[p]
		fd := FileDiff{Path: p}
		switch {
		case okA && !okB:
			fd.Status, fd.OldSize = "removed", fa.Size
		case !okA && okB:
			fd.Status, fd.NewSize = "added", fb.Size
		case fa.SHA256 == fb.SHA256:
			fd.Status, fd.OldSize, fd.NewSize = "unchanged", fa.Size, fb.Size
		default:
			fd.Status, fd.OldSize, fd.NewSize = "modified", fa.Size, fb.Size
		}
		mime := fb.Mime
		if !okB {
			mime = fa.Mime
		}
		if fd.Status != "unchanged" {
			d.Changed++
			if !IsText(mime) {
				fd.Binary = true
			} else {
				var oldS, newS string
				if okA {
					bs, err := s.Blobs.Read(fa.SHA256)
					if err != nil {
						return nil, err
					}
					oldS = string(bs)
				}
				if okB {
					bs, err := s.Blobs.Read(fb.SHA256)
					if err != nil {
						return nil, err
					}
					newS = string(bs)
				}
				if !utf8.ValidString(oldS) || !utf8.ValidString(newS) {
					fd.Binary = true
				} else {
					diffText(&fd, oldS, newS, from, to, withRows)
				}
			}
			d.Added += fd.Added
			d.Removed += fd.Removed
		}
		d.Files = append(d.Files, fd)
	}
	return d, nil
}

func diffText(fd *FileDiff, oldS, newS string, from, to int, withRows bool) {
	al, bl := splitLines(oldS), splitLines(newS)
	if len(al) > maxDiffLines || len(bl) > maxDiffLines {
		fd.TooBig = true
		return
	}
	m := difflib.NewMatcher(al, bl)
	for _, op := range m.GetOpCodes() {
		switch op.Tag {
		case 'r':
			fd.Removed += op.I2 - op.I1
			fd.Added += op.J2 - op.J1
		case 'd':
			fd.Removed += op.I2 - op.I1
		case 'i':
			fd.Added += op.J2 - op.J1
		}
	}
	u, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: al, B: bl, FromFile: fmt.Sprintf("v%d/%s", from, fd.Path), ToFile: fmt.Sprintf("v%d/%s", to, fd.Path), Context: 3,
	})
	if len(u) > 400000 {
		u = u[:400000] + "\n…（对比结果过长，已截断）\n"
	}
	fd.Unified = u
	if !withRows {
		return
	}
	trim := func(s string) string { return strings.TrimRight(s, "\r\n") }
	groups := m.GetGroupedOpCodes(3)
	lastI := 0
	for gi, g := range groups {
		if len(g) == 0 {
			continue
		}
		if g[0].I1 > lastI || (gi == 0 && g[0].I1 > 0) {
			fd.Rows = append(fd.Rows, DiffRow{Kind: "skip", Skip: g[0].I1 - lastI})
		}
		for _, op := range g {
			switch op.Tag {
			case 'e':
				for k := 0; k < op.I2-op.I1; k++ {
					fd.Rows = append(fd.Rows, DiffRow{Kind: "eq", LN: op.I1 + k + 1, RN: op.J1 + k + 1, L: trim(al[op.I1+k]), R: trim(bl[op.J1+k])})
				}
			case 'd':
				for k := op.I1; k < op.I2; k++ {
					fd.Rows = append(fd.Rows, DiffRow{Kind: "del", LN: k + 1, L: trim(al[k])})
				}
			case 'i':
				for k := op.J1; k < op.J2; k++ {
					fd.Rows = append(fd.Rows, DiffRow{Kind: "ins", RN: k + 1, R: trim(bl[k])})
				}
			case 'r':
				n := max(op.I2-op.I1, op.J2-op.J1)
				for k := 0; k < n; k++ {
					row := DiffRow{Kind: "chg"}
					if op.I1+k < op.I2 {
						row.LN, row.L = op.I1+k+1, trim(al[op.I1+k])
					}
					if op.J1+k < op.J2 {
						row.RN, row.R = op.J1+k+1, trim(bl[op.J1+k])
					}
					if row.LN > 0 && row.RN > 0 {
						row.LSeg, row.RSeg, row.Inline = inlineDiff(row.L, row.R)
					}
					fd.Rows = append(fd.Rows, row)
				}
			}
		}
		lastI = g[len(g)-1].I2
	}
	if lastI < len(al) && len(groups) > 0 {
		fd.Rows = append(fd.Rows, DiffRow{Kind: "skip", Skip: len(al) - lastI})
	}
	if len(fd.Rows) > 8000 {
		fd.Rows = fd.Rows[:8000]
	}
}
