// Package config 读取服务的启动参数。所有参数都来自环境变量，便于在 Docker 中配置。
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr          string // 监听地址，如 :8790
	PublicURL     string // 对外访问地址，不带结尾斜杠，如 http://64.83.26.230:8790
	ContentOrigin string // 可选：artifact 内容使用的独立源，如 http://64.83.26.230:8791
	DataDir       string
	AdminUser     string
	AdminPassword string
	Secret        string // HMAC 密钥；为空时自动生成并保存在数据库里
	MinFreeDiskMB int64  // 磁盘剩余低于该值时拒绝写入
	TrustProxy    bool   // 是否信任 X-Forwarded-For / X-Forwarded-Proto
	MaxBodyMB     int64  // 单个请求体上限

	// 对象存储（S3 兼容，如 Cloudflare R2）。S3Bucket 非空时文件内容存放到对象存储，否则存本地 DataDir/blobs。
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3Prefix    string
}

// S3Enabled 表示文件内容是否存放在对象存储。
func (c *Config) S3Enabled() bool { return c.S3Bucket != "" }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int64) int64 {
	v := env(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// Load 从环境变量构造配置并做基本校验。
func Load() (*Config, error) {
	port := env("ARTIFACT_PORT", "8790")
	c := &Config{
		Addr:          env("ARTIFACT_ADDR", ":"+port),
		PublicURL:     strings.TrimRight(env("ARTIFACT_PUBLIC_URL", "http://localhost:"+port), "/"),
		ContentOrigin: strings.TrimRight(env("ARTIFACT_CONTENT_ORIGIN", ""), "/"),
		DataDir:       env("ARTIFACT_DATA_DIR", "./data"),
		AdminUser:     env("ARTIFACT_ADMIN_USER", "admin"),
		AdminPassword: env("ARTIFACT_ADMIN_PASSWORD", ""),
		Secret:        env("ARTIFACT_SECRET", ""),
		MinFreeDiskMB: envInt("ARTIFACT_MIN_FREE_DISK_MB", 300),
		TrustProxy:    envBool("ARTIFACT_TRUST_PROXY", false),
		MaxBodyMB:     envInt("ARTIFACT_MAX_BODY_MB", 64),
		S3Endpoint:    strings.TrimRight(env("ARTIFACT_S3_ENDPOINT", ""), "/"),
		S3Bucket:      env("ARTIFACT_S3_BUCKET", ""),
		S3AccessKey:   env("ARTIFACT_S3_ACCESS_KEY", ""),
		S3SecretKey:   env("ARTIFACT_S3_SECRET_KEY", ""),
		S3Region:      env("ARTIFACT_S3_REGION", "auto"),
		S3Prefix:      env("ARTIFACT_S3_PREFIX", ""),
	}
	if _, err := url.ParseRequestURI(c.PublicURL); err != nil {
		return nil, fmt.Errorf("ARTIFACT_PUBLIC_URL 无效：%w", err)
	}
	if c.ContentOrigin != "" {
		if _, err := url.ParseRequestURI(c.ContentOrigin); err != nil {
			return nil, fmt.Errorf("ARTIFACT_CONTENT_ORIGIN 无效：%w", err)
		}
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.DataDir = abs
	return c, nil
}

// Origin 返回 PublicURL 的 scheme://host[:port] 部分。
func (c *Config) Origin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return c.PublicURL
	}
	return u.Scheme + "://" + u.Host
}

// ContentBase 返回 artifact 内容使用的源；未单独配置时与 PublicURL 同源。
func (c *Config) ContentBase() string {
	if c.ContentOrigin != "" {
		return c.ContentOrigin
	}
	return c.Origin()
}
