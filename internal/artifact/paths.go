package artifact

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CleanPath 规范化包内文件路径，拒绝越界、隐藏文件和会破坏 URL 的字符。
func CleanPath(p string) (string, error) {
	orig := p
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	if p == "" {
		return "", fmt.Errorf("文件路径为空")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("文件路径 %q 不能以 / 开头，请使用相对路径", orig)
	}
	if len(p) > 200 {
		return "", fmt.Errorf("文件路径 %q 过长", orig)
	}
	if !utf8.ValidString(p) {
		return "", fmt.Errorf("文件路径 %q 不是合法的 UTF-8", orig)
	}
	segs := strings.Split(p, "/")
	if len(segs) > 8 {
		return "", fmt.Errorf("文件路径 %q 层级过深（最多 8 层）", orig)
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("文件路径 %q 不合法", orig)
		}
		if strings.HasPrefix(s, ".") {
			return "", fmt.Errorf("文件路径 %q 含隐藏文件或目录", orig)
		}
		for _, r := range s {
			if unicode.IsControl(r) || strings.ContainsRune(`:*?"<>|#%`, r) {
				return "", fmt.Errorf("文件路径 %q 含不允许的字符 %q", orig, r)
			}
		}
	}
	return path.Clean(p), nil
}

var mimeByExt = map[string]string{
	".html":     "text/html; charset=utf-8",
	".htm":      "text/html; charset=utf-8",
	".css":      "text/css; charset=utf-8",
	".js":       "text/javascript; charset=utf-8",
	".mjs":      "text/javascript; charset=utf-8",
	".json":     "application/json; charset=utf-8",
	".map":      "application/json; charset=utf-8",
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
	".txt":      "text/plain; charset=utf-8",
	".csv":      "text/csv; charset=utf-8",
	".tsv":      "text/tab-separated-values; charset=utf-8",
	".xml":      "application/xml; charset=utf-8",
	".svg":      "image/svg+xml",
	".png":      "image/png",
	".jpg":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".gif":      "image/gif",
	".webp":     "image/webp",
	".avif":     "image/avif",
	".ico":      "image/x-icon",
	".bmp":      "image/bmp",
	".woff":     "font/woff",
	".woff2":    "font/woff2",
	".ttf":      "font/ttf",
	".otf":      "font/otf",
	".mp3":      "audio/mpeg",
	".ogg":      "audio/ogg",
	".wav":      "audio/wav",
	".m4a":      "audio/mp4",
	".mp4":      "video/mp4",
	".webm":     "video/webm",
	".pdf":      "application/pdf",
	".wasm":     "application/wasm",
	".glb":      "model/gltf-binary",
	".gltf":     "model/gltf+json",
	".geojson":  "application/geo+json; charset=utf-8",
	".yaml":     "text/yaml; charset=utf-8",
	".yml":      "text/yaml; charset=utf-8",
}

// DetectMime 按扩展名判断类型；未知扩展名的 UTF-8 文本按纯文本处理，其他二进制拒绝。
func DetectMime(p string, data []byte) (string, error) {
	ext := strings.ToLower(path.Ext(p))
	if m, ok := mimeByExt[ext]; ok {
		return m, nil
	}
	if utf8.Valid(data) {
		return "text/plain; charset=utf-8", nil
	}
	return "", fmt.Errorf("不支持的文件类型 %q（只接受网页、样式、脚本、数据、图片、字体、音视频和 PDF）", p)
}

// IsText 判断文件是否按文本处理（可做 diff、局部编辑和检查）。
func IsText(mime string) bool {
	return strings.HasPrefix(mime, "text/") || strings.Contains(mime, "json") || strings.Contains(mime, "xml") ||
		strings.Contains(mime, "javascript") || strings.HasPrefix(mime, "image/svg")
}

// KindOf 由入口文件推断 artifact 类型。
func KindOf(entry string) (string, error) {
	switch strings.ToLower(path.Ext(entry)) {
	case ".html", ".htm":
		return "html", nil
	case ".md", ".markdown":
		return "markdown", nil
	case ".svg":
		return "svg", nil
	}
	return "", fmt.Errorf("入口文件 %q 必须是 .html、.md 或 .svg", entry)
}
