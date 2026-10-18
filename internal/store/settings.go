package store

import (
	"encoding/json"
	"errors"
)

// Settings 是可在管理后台修改的全局设置。
type Settings struct {
	DefaultVisibility  string   `json:"default_visibility"`
	QuotaStorageMB     int64    `json:"quota_storage_mb"`      // 每用户存储上限（去重后）
	QuotaArtifacts     int64    `json:"quota_artifacts"`       // 每用户 artifact 数上限
	QuotaFileMB        int64    `json:"quota_file_mb"`         // 单文件上限
	QuotaVersionMB     int64    `json:"quota_version_mb"`      // 单个版本总大小上限
	QuotaFiles         int64    `json:"quota_files"`           // 单个版本的文件数上限
	QuotaReqPerMin     int64    `json:"quota_req_per_min"`     // 每个 Key 每分钟请求数
	QuotaPublishPerMin int64    `json:"quota_publish_per_min"` // 每个用户每分钟发布次数
	MaxVersions        int64    `json:"max_versions"`          // 每个 artifact 保留的版本数，0 表示不限
	TrashDays          int64    `json:"trash_days"`            // 回收站保留天数，0 表示不自动清理
	CDNAllowlist       []string `json:"cdn_allowlist"`         // artifact 允许加载脚本/样式的外部源
	AllowSelfKeys      bool     `json:"allow_self_keys"`       // 普通用户能否自行签发 Key
}

func DefaultSettings() Settings {
	return Settings{
		DefaultVisibility:  "unlisted",
		QuotaStorageMB:     200,
		QuotaArtifacts:     300,
		QuotaFileMB:        10,
		QuotaVersionMB:     30,
		QuotaFiles:         300,
		QuotaReqPerMin:     180,
		QuotaPublishPerMin: 20,
		MaxVersions:        0,
		TrashDays:          30,
		CDNAllowlist: []string{
			"https://cdnjs.cloudflare.com",
			"https://cdn.jsdelivr.net",
			"https://unpkg.com",
			"https://fonts.googleapis.com",
			"https://fonts.gstatic.com",
		},
		AllowSelfKeys: true,
	}
}

func (s *Store) Settings() Settings {
	s.settingsMu.RLock()
	if s.settingsCache != nil {
		v := *s.settingsCache
		s.settingsMu.RUnlock()
		return v
	}
	s.settingsMu.RUnlock()
	st := DefaultSettings()
	if raw, err := s.GetKV("settings"); err == nil {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	s.settingsMu.Lock()
	s.settingsCache = &st
	s.settingsMu.Unlock()
	return st
}

func (s *Store) SaveSettings(st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	if err := s.SetKV("settings", string(b)); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settingsCache = &st
	s.settingsMu.Unlock()
	return nil
}

func (st Settings) Validate() error {
	switch st.DefaultVisibility {
	case "private", "unlisted", "public":
	default:
		return errors.New("默认可见性必须是 private、unlisted 或 public")
	}
	if st.QuotaStorageMB <= 0 || st.QuotaArtifacts <= 0 || st.QuotaFileMB <= 0 || st.QuotaVersionMB <= 0 || st.QuotaFiles <= 0 || st.QuotaReqPerMin <= 0 || st.QuotaPublishPerMin <= 0 {
		return errors.New("各项限额必须大于 0")
	}
	if st.MaxVersions < 0 || st.TrashDays < 0 {
		return errors.New("版本保留数和回收站天数不能为负数")
	}
	return nil
}
