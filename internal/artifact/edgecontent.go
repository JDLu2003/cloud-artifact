package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"vps-artifact/internal/store"
)

// 内容分发：文件存在 R2 上时，浏览器不经过服务器，由 Cloudflare Worker 直接从 R2 读取内容。
// Worker 不能访问数据库，所以写节点在每次变更时把它需要的信息写进 R2：
//
//	_m/<id>.json          {visibility, current, deleted}    —— 访问控制
//	_m/<id>/v<版本>.json   {entry, files: {路径: {mime, sha256, rendered}}} —— 版本清单
//	_r/<id>/v<版本>/<路径>  包装后的 HTML、渲染好的 Markdown 页面
//	_m/_settings.json      {cdn_allowlist, public_url}       —— 生成 CSP 用
//
// 原始文件仍在 <id>/v<版本>/<路径>。

// EdgeMeta 是 _m/<id>.json 的内容。
type EdgeMeta struct {
	Visibility string `json:"visibility"`
	Current    int    `json:"current"`
	Deleted    bool   `json:"deleted"`
}

// EdgeFile 是版本清单里的一个文件。
type EdgeFile struct {
	Mime     string `json:"mime"`
	SHA256   string `json:"sha256"`
	Rendered bool   `json:"rendered,omitempty"` // 有 _r/ 下的渲染结果
}

type EdgeManifest struct {
	Entry string              `json:"entry"`
	Files map[string]EdgeFile `json:"files"`
}

type EdgeSettings struct {
	CDNAllowlist []string `json:"cdn_allowlist"`
	PublicURL    string   `json:"public_url"`
}

// MarkdownRenderer 把 Markdown 渲染成完整页面（由 httpapi 提供模板）。
type MarkdownRenderer func(art *store.Artifact, src []byte) ([]byte, error)

// EdgeEnabled 表示是否需要维护内容分发对象。
func (s *Service) EdgeEnabled() bool { return s.Blobs != nil && s.Blobs.Shared() }

func metaKey(id string) string              { return "_m/" + id + ".json" }
func manifestKey(id string, ver int) string { return fmt.Sprintf("_m/%s/v%d.json", id, ver) }
func renderKey(id string, ver int, p string) string {
	return fmt.Sprintf("_r/%s/v%d/%s", id, ver, p)
}

// needsRender 判断文件在直接查看时是否需要渲染（HTML 包装、Markdown 转页面）。
func needsRender(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm", ".md", ".markdown":
		return true
	}
	return false
}

func (s *Service) renderFor(art *store.Artifact, p string, data []byte) ([]byte, error) {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm":
		return WrapHTML(data), nil
	default:
		if s.RenderMarkdown == nil {
			return nil, fmt.Errorf("未设置 Markdown 渲染")
		}
		return s.RenderMarkdown(art, data)
	}
}

// edgeWriteVersion 写入一个版本的清单和渲染结果。content 提供需要渲染的文件内容（缺失的从存储读取）。
// 在元数据提交前调用：此时 _m/<id>.json 还没有指向这个版本，写入失败不会影响读者。
func (s *Service) edgeWriteVersion(art *store.Artifact, ver int, entry string, files []store.VersionFile, content map[string][]byte) error {
	m := EdgeManifest{Entry: entry, Files: map[string]EdgeFile{}}
	for _, f := range files {
		ef := EdgeFile{Mime: f.Mime, SHA256: f.SHA256}
		if needsRender(f.Path) {
			data, ok := content[f.Path]
			if !ok {
				var err error
				if data, err = s.Blobs.Read(f.SHA256); err != nil {
					return fmt.Errorf("读取 %s: %w", f.Path, err)
				}
			}
			out, err := s.renderFor(art, f.Path, data)
			if err != nil {
				return fmt.Errorf("渲染 %s: %w", f.Path, err)
			}
			if err := s.Blobs.PutEdge(renderKey(art.ID, ver, f.Path), out, "text/html; charset=utf-8"); err != nil {
				return err
			}
			ef.Rendered = true
		}
		m.Files[f.Path] = ef
	}
	b, _ := json.Marshal(m)
	return s.Blobs.PutEdge(manifestKey(art.ID, ver), b, "application/json")
}

// edgeWriteMeta 根据数据库里的最新状态写 _m/<id>.json；artifact 已彻底删除时删除它的所有分发对象。
func (s *Service) edgeWriteMeta(id string) error {
	art, err := s.Store.ArtifactByID(id)
	if err == store.ErrNotFound {
		return s.edgePurge(id)
	}
	if err != nil {
		return err
	}
	b, _ := json.Marshal(EdgeMeta{Visibility: art.Visibility, Current: art.CurrentVersion, Deleted: art.DeletedAt != nil})
	return s.Blobs.PutEdge(metaKey(id), b, "application/json")
}

func (s *Service) edgePurge(id string) error {
	// 先删访问控制信息：之后 Worker 对这个 artifact 一律返回 404
	if err := s.Blobs.DeleteEdge(metaKey(id)); err != nil {
		return err
	}
	if err := s.Blobs.DeleteEdgePrefix("_m/" + id + "/"); err != nil {
		return err
	}
	return s.Blobs.DeleteEdgePrefix("_r/" + id + "/")
}

// edgeAfterChange 在元数据变更后同步访问控制信息。失败时记录日志并稍后重试一次；
// 每小时的全量同步（EdgeResync）会兜底。
func (s *Service) edgeAfterChange(id string) {
	if !s.EdgeEnabled() {
		return
	}
	if err := s.edgeWriteMeta(id); err != nil {
		slog.Error("写入内容分发信息失败，稍后重试", "id", id, "err", err)
		go func() {
			time.Sleep(10 * time.Second)
			if err := s.edgeWriteMeta(id); err != nil {
				slog.Error("重试写入内容分发信息失败", "id", id, "err", err)
			}
		}()
	}
}

// EdgeWriteSettings 写入 Worker 生成 CSP 需要的设置。
func (s *Service) EdgeWriteSettings() error {
	if !s.EdgeEnabled() {
		return nil
	}
	b, _ := json.Marshal(EdgeSettings{CDNAllowlist: s.Store.Settings().CDNAllowlist, PublicURL: s.Cfg.PublicURL})
	return s.Blobs.PutEdge("_m/_settings.json", b, "application/json")
}

// EdgeResync 重写所有 artifact 的访问控制信息；full 为 true 时同时补齐每个版本的清单和渲染结果。
// 返回处理的 artifact 数。
func (s *Service) EdgeResync(ctx context.Context, full bool) (int, error) {
	if !s.EdgeEnabled() {
		return 0, fmt.Errorf("没有配置 R2，不需要内容分发")
	}
	if err := s.EdgeWriteSettings(); err != nil {
		return 0, err
	}
	ids, err := s.Store.AllArtifactIDs()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if full {
			art, err := s.Store.ArtifactByID(id)
			if err != nil {
				return n, err
			}
			vs, err := s.Store.Versions(id)
			if err != nil {
				return n, err
			}
			for _, v := range vs {
				files, err := s.Store.VersionFiles(id, v.Number)
				if err != nil {
					return n, err
				}
				if err := s.edgeWriteVersion(art, v.Number, v.Entry, files, nil); err != nil {
					return n, fmt.Errorf("%s v%d: %w", id, v.Number, err)
				}
			}
		}
		if err := s.edgeWriteMeta(id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// edgeRerenderMarkdown 在标题变化后重新渲染当前版本的 Markdown 页面（页面里显示标题）。
func (s *Service) edgeRerenderMarkdown(id string) {
	if !s.EdgeEnabled() {
		return
	}
	art, err := s.Store.ArtifactByID(id)
	if err != nil {
		return
	}
	v, err := s.Store.VersionByNumber(id, art.CurrentVersion)
	if err != nil {
		return
	}
	files, err := s.Store.VersionFiles(id, v.Number)
	if err != nil {
		return
	}
	if err := s.edgeWriteVersion(art, v.Number, v.Entry, files, nil); err != nil {
		slog.Error("重新渲染失败", "id", id, "err", err)
	}
}
