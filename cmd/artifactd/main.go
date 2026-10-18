// artifactd 是 Artifact 服务的主程序。
//
//	artifactd serve                         启动服务
//	artifactd user add <名称> [--admin] [--password 密码]
//	artifactd user list
//	artifactd user passwd <名称> [--password 密码]
//	artifactd key issue <用户名> [--name 名称]
//	artifactd key list [用户名]
//	artifactd key revoke <ID>
//	artifactd prompt <用户名>               签发新 Key 并打印接入提示词
//	artifactd check <文件或目录>            本地运行发布前检查
//	artifactd gc                            清理无引用文件和过期回收站
//	artifactd backup <文件>                 在线备份数据库
//	artifactd version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	assets "vps-artifact"
	"vps-artifact/internal/artifact"
	"vps-artifact/internal/auth"
	"vps-artifact/internal/blob"
	"vps-artifact/internal/config"
	"vps-artifact/internal/httpapi"
	"vps-artifact/internal/lint"
	"vps-artifact/internal/metrics"
	"vps-artifact/internal/skillpack"
	"vps-artifact/internal/store"
)

var Version = "0.1.0"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve"}
	}
	var err error
	switch args[0] {
	case "serve":
		err = serve()
	case "user":
		err = cmdUser(args[1:])
	case "key":
		err = cmdKey(args[1:])
	case "prompt":
		err = cmdPrompt(args[1:])
	case "check":
		err = cmdCheck(args[1:])
	case "gc":
		err = cmdGC()
	case "backup":
		err = cmdBackup(args[1:])
	case "version", "-v", "--version":
		fmt.Println("artifactd", Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`用法：artifactd <命令>

  serve                              启动服务（默认）
  user add <名称> [--admin] [--password 密码]
  user list
  user passwd <名称> [--password 密码]
  key issue <用户名> [--name 名称]
  key list [用户名]
  key revoke <ID>
  prompt <用户名>                    签发新 Key 并打印给 agent 的接入提示词
  check <文件或目录>                 本地运行发布前检查
  gc                                 清理无引用文件和过期回收站
  backup <文件>                      在线备份数据库（blobs/ 目录另行复制）
  version

配置通过环境变量提供，见 .env.example。
`)
}

type app struct {
	cfg    *config.Config
	store  *store.Store
	blobs  *blob.Store
	signer *auth.Signer
	svc    *artifact.Service
	libFS  fs.FS
}

func open() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	bl, err := blob.New(filepath.Join(cfg.DataDir, "blobs"))
	if err != nil {
		return nil, err
	}
	secret := cfg.Secret
	if secret == "" {
		secret, err = st.GetKV("secret")
		if errors.Is(err, store.ErrNotFound) {
			secret = auth.RandomString(48)
			if err := st.SetKV("secret", secret); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
	}
	libFS, _ := fs.Sub(assets.FS, "web/lib")
	signer := auth.NewSigner(secret)
	svc := &artifact.Service{Store: st, Blobs: bl, Cfg: cfg, Signer: signer, LibExists: httpapi.LibExists(libFS)}
	return &app{cfg: cfg, store: st, blobs: bl, signer: signer, svc: svc, libFS: libFS}, nil
}

func serve() error {
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	skillFS, _ := fs.Sub(assets.FS, "skill/artifact")
	pack, err := skillpack.New(skillFS)
	if err != nil {
		return err
	}
	adminFS, _ := fs.Sub(assets.FS, "web/admin")
	viewerFS, _ := fs.Sub(assets.FS, "web/viewer")
	srv := &httpapi.Server{
		Cfg: a.cfg, Store: a.store, Svc: a.svc, Signer: a.signer, Limiter: auth.NewLimiter(), Metrics: metrics.New(),
		Skill: pack, Version: Version, LibFS: a.libFS, AdminFS: adminFS, ViewerFS: viewerFS,
	}
	if err := srv.Init(); err != nil {
		return err
	}
	if err := bootstrap(a, srv); err != nil {
		return err
	}
	go housekeeping(a)

	hs := &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("Artifact 服务已启动", "addr", a.cfg.Addr, "public_url", a.cfg.PublicURL, "data", a.cfg.DataDir, "version", Version)
		errc <- hs.ListenAndServe()
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-sig:
		slog.Info("正在停止服务…")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return hs.Shutdown(ctx)
	}
	return nil
}

// bootstrap 在第一次启动时创建管理员和一个 Key，并打印接入提示词。
func bootstrap(a *app, srv *httpapi.Server) error {
	n, err := a.store.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pw := a.cfg.AdminPassword
	generated := false
	if pw == "" {
		pw = auth.RandomString(16)
		generated = true
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("ARTIFACT_ADMIN_PASSWORD 不符合要求：%w", err)
	}
	u, err := a.store.CreateUser(store.NewUser{Name: a.cfg.AdminUser, DisplayName: "管理员", PasswordHash: hash, Role: "admin"})
	if err != nil {
		return err
	}
	key, _, err := srv.IssueKey(u.ID, "初始 Key", 0)
	if err != nil {
		return err
	}
	a.store.Audit(&u.ID, nil, "", "bootstrap", u.Name, "首次启动创建管理员")
	line := strings.Repeat("=", 72)
	fmt.Println(line)
	fmt.Println("Artifact 服务首次启动，已创建管理员。以下信息只显示这一次，请妥善保存。")
	fmt.Println()
	fmt.Printf("管理后台：%s/admin/\n", a.cfg.PublicURL)
	fmt.Printf("用户名：  %s\n", u.Name)
	if generated {
		fmt.Printf("密码：    %s\n", pw)
	} else {
		fmt.Println("密码：    （使用 ARTIFACT_ADMIN_PASSWORD 中设置的值）")
	}
	fmt.Println()
	fmt.Println("把下面这段提示词粘贴给你的 agent（pi、Claude Code、Codex 等），它会自行安装技能并接入：")
	fmt.Println(line)
	fmt.Print(skillpack.Prompt(a.cfg.PublicURL, key, u.Name))
	fmt.Println(line)
	fmt.Println("为其他人或其他 agent 签发 Key：在管理后台「密钥」页操作，或运行 artifactd prompt <用户名>。")
	fmt.Println(line)
	return nil
}

func housekeeping(a *app) {
	t := time.NewTicker(time.Hour)
	run := func() {
		a.store.PurgeExpiredSessions()
		a.store.TrimAudit(50000)
		if n, freed, err := a.svc.GC(); err == nil && n > 0 {
			slog.Info("清理无引用文件", "files", n, "freed", auth.FormatBytes(freed))
		}
	}
	time.Sleep(time.Minute)
	run()
	for range t.C {
		run()
	}
}

// ---- 命令行管理 ----

func cmdUser(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：artifactd user add|list|passwd ...")
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	switch args[0] {
	case "list":
		users, err := a.store.ListUsers()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\t用户名\t角色\t状态\tartifact\t存储\tKey")
		for _, u := range users {
			state := "正常"
			if u.Disabled {
				state = "停用"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%s\t%d\n", u.ID, u.Name, u.Role, state, u.ArtifactCount, auth.FormatBytes(u.StorageBytes), u.KeyCount)
		}
		return tw.Flush()
	case "add":
		fs := flag.NewFlagSet("user add", flag.ContinueOnError)
		admin := fs.Bool("admin", false, "设为管理员")
		pw := fs.String("password", "", "登录密码（留空则随机生成）")
		if len(args) < 2 {
			return errors.New("用法：artifactd user add <名称> [--admin] [--password 密码]")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		pass := *pw
		if pass == "" {
			pass = auth.RandomString(14)
		}
		hash, err := auth.HashPassword(pass)
		if err != nil {
			return err
		}
		role := "user"
		if *admin {
			role = "admin"
		}
		u, err := a.store.CreateUser(store.NewUser{Name: args[1], PasswordHash: hash, Role: role})
		if err != nil {
			return err
		}
		a.store.Audit(nil, nil, "cli", "user.create", u.Name, role)
		fmt.Printf("已创建用户 %s（%s），密码：%s\n", u.Name, role, pass)
		return nil
	case "passwd":
		fs := flag.NewFlagSet("user passwd", flag.ContinueOnError)
		pw := fs.String("password", "", "新密码（留空则随机生成）")
		if len(args) < 2 {
			return errors.New("用法：artifactd user passwd <名称> [--password 密码]")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		u, err := a.store.UserByName(args[1])
		if err != nil {
			return fmt.Errorf("用户 %s 不存在", args[1])
		}
		pass := *pw
		if pass == "" {
			pass = auth.RandomString(14)
		}
		hash, err := auth.HashPassword(pass)
		if err != nil {
			return err
		}
		if err := a.store.SetPassword(u.ID, hash); err != nil {
			return err
		}
		a.store.Audit(nil, nil, "cli", "user.password", u.Name, "命令行重置")
		fmt.Printf("已重置 %s 的密码：%s\n", u.Name, pass)
		return nil
	}
	return errors.New("未知子命令：" + args[0])
}

func cmdKey(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：artifactd key issue|list|revoke ...")
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	srv := &httpapi.Server{Store: a.store}
	switch args[0] {
	case "issue":
		fs := flag.NewFlagSet("key issue", flag.ContinueOnError)
		name := fs.String("name", "agent", "Key 名称")
		if len(args) < 2 {
			return errors.New("用法：artifactd key issue <用户名> [--name 名称]")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		u, err := a.store.UserByName(args[1])
		if err != nil {
			return fmt.Errorf("用户 %s 不存在", args[1])
		}
		key, k, err := srv.IssueKey(u.ID, *name, 0)
		if err != nil {
			return err
		}
		a.store.Audit(nil, nil, "cli", "key.issue", u.Name, k.Prefix)
		fmt.Println(key)
		return nil
	case "list":
		var uid int64
		if len(args) > 1 {
			u, err := a.store.UserByName(args[1])
			if err != nil {
				return fmt.Errorf("用户 %s 不存在", args[1])
			}
			uid = u.ID
		}
		keys, err := a.store.ListKeys(uid)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\t用户\t名称\t前缀\t状态\t最后使用")
		now := time.Now().Unix()
		for _, k := range keys {
			state := "有效"
			if !k.Active(now) {
				state = "失效"
			}
			last := "-"
			if k.LastUsedAt != nil {
				last = time.Unix(*k.LastUsedAt, 0).Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s…\t%s\t%s\n", k.ID, k.UserName, k.Name, k.Prefix, state, last)
		}
		return tw.Flush()
	case "revoke":
		if len(args) < 2 {
			return errors.New("用法：artifactd key revoke <ID>")
		}
		var id int64
		fmt.Sscan(args[1], &id)
		if err := a.store.RevokeKey(id); err != nil {
			return err
		}
		a.store.Audit(nil, nil, "cli", "key.revoke", args[1], "")
		fmt.Println("已吊销")
		return nil
	}
	return errors.New("未知子命令：" + args[0])
}

func cmdPrompt(args []string) error {
	if len(args) < 1 {
		return errors.New("用法：artifactd prompt <用户名>")
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	u, err := a.store.UserByName(args[0])
	if err != nil {
		return fmt.Errorf("用户 %s 不存在", args[0])
	}
	srv := &httpapi.Server{Store: a.store}
	key, k, err := srv.IssueKey(u.ID, "agent", 0)
	if err != nil {
		return err
	}
	a.store.Audit(nil, nil, "cli", "key.issue", u.Name, k.Prefix)
	fmt.Print(skillpack.Prompt(a.cfg.PublicURL, key, u.Name))
	return nil
}

func cmdCheck(args []string) error {
	if len(args) < 1 {
		return errors.New("用法：artifactd check <文件或目录>")
	}
	root := args[0]
	st, err := os.Stat(root)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	all := map[string]bool{}
	entry := ""
	if st.IsDir() {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != root {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			all[rel] = true
			if ext := strings.ToLower(filepath.Ext(p)); ext == ".html" || ext == ".md" || ext == ".svg" {
				b, _ := os.ReadFile(p)
				files[rel] = b
			}
			return nil
		})
		for _, c := range []string{"index.html", "index.md", "README.md"} {
			if all[c] {
				entry = c
				break
			}
		}
	} else {
		b, err := os.ReadFile(root)
		if err != nil {
			return err
		}
		entry = filepath.Base(root)
		files[entry] = b
		all[entry] = true
	}
	libFS, _ := fs.Sub(assets.FS, "web/lib")
	ws := lint.Check(lint.Input{Files: files, AllPaths: all, Entry: entry, Allowlist: store.DefaultSettings().CDNAllowlist, LibExists: httpapi.LibExists(libFS)})
	if len(ws) == 0 {
		fmt.Println("没有发现问题。")
		return nil
	}
	for _, w := range ws {
		fmt.Printf("[%s] %s %s：%s\n", w.Level, w.File, w.Rule, w.Message)
		for _, e := range w.Examples {
			fmt.Printf("    · %s\n", e)
		}
	}
	return nil
}

func cmdGC() error {
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	n, freed, err := a.svc.GC()
	if err != nil {
		return err
	}
	fmt.Printf("删除 %d 个无引用文件，释放 %s\n", n, auth.FormatBytes(freed))
	return nil
}

func cmdBackup(args []string) error {
	if len(args) < 1 {
		return errors.New("用法：artifactd backup <目标文件>，例如 /data/backup.db")
	}
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	if _, err := os.Stat(args[0]); err == nil {
		return fmt.Errorf("目标文件 %s 已存在", args[0])
	}
	if _, err := a.store.DB.Exec(`VACUUM INTO ?`, args[0]); err != nil {
		return err
	}
	fmt.Printf("数据库已备份到 %s。文件内容在 %s，请一并复制。\n", args[0], filepath.Join(a.cfg.DataDir, "blobs"))
	return nil
}
