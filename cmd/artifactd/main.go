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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/JDLu2003/vps-cluster"
	"github.com/JDLu2003/vps-cluster/s3"
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
	case "content-key":
		err = cmdContentKey()
	case "backfill-render":
		err = cmdBackfillRender()
	case "migrate-blobs":
		err = cmdMigrateBlobs(args[1:])
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
  migrate-blobs [--purge-legacy]     把旧版 blobs/ 布局迁到 <artifact_id>/v<版本>/<路径>（可重复执行）
  content-key                        输出内容令牌的签名密钥（配置 Cloudflare Worker 用，不能用来登录）
  backfill-render                    文件在 R2 上时，为已有版本补齐 Worker 需要的清单与渲染结果（可重复执行）
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
	r2     *s3.Client // 可选：R2 只用于备份和取回缺失文件
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
	// 配置了 ARTIFACT_S3_* 时，artifact 文件直接写入 R2（即时上传，所有机器共用），
	// 集群只复制数据库里的控制信息；没有配置时文件存本机，由机器之间互相推送。
	libFS, _ := fs.Sub(assets.FS, "web/lib")
	skillpack.Repo, skillpack.Ref = cfg.SkillRepo, cfg.SkillRef
	a := &app{cfg: cfg, store: st, libFS: libFS}
	var bl *blob.Store
	if c, ok := r2Config(a); ok {
		if bl, err = blob.NewS3(blob.S3Config{Endpoint: c.Endpoint, Bucket: c.Bucket, AccessKey: c.AccessKey,
			SecretKey: c.SecretKey, Region: c.Region, Prefix: c.Prefix}); err != nil {
			return nil, err
		}
		if a.r2, err = s3.New(c); err != nil {
			return nil, err
		}
	} else if bl, err = blob.New(filepath.Join(cfg.DataDir, "blobs")); err != nil {
		return nil, err
	}
	bl.SetLocator(st.BlobLocation)
	a.blobs = bl
	a.svc = &artifact.Service{Store: st, Blobs: bl, Cfg: cfg, LibExists: httpapi.LibExists(libFS), SharedBlobs: a.r2 != nil}
	return a, nil
}

// initSecret 读取（首次时生成）签名密钥。集群模式下密钥保存在复制的数据库里，所有机器一致。
func initSecret(a *app) error {
	secret := a.cfg.Secret
	if secret == "" {
		var err error
		secret, err = a.store.GetKV("secret")
		if errors.Is(err, store.ErrNotFound) {
			secret = auth.RandomString(48)
			if err := a.store.SetKV("secret", secret); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	a.signer = auth.NewSigner(secret)
	a.svc.Signer = a.signer
	return nil
}

// openCLI 供命令行子命令使用。集群模式下命令行直接写本机数据库会让副本分叉，因此拒绝写操作。
func openCLI(write bool) (*app, error) {
	if write && clusterEnabled() {
		return nil, errors.New("集群模式下请在管理后台操作（命令行写入不会同步到其他机器）")
	}
	a, err := open()
	if err != nil {
		return nil, err
	}
	if err := initSecret(a); err != nil {
		a.store.Close()
		return nil, err
	}
	return a, nil
}

func serve() error {
	a, err := open()
	if err != nil {
		return err
	}
	defer a.store.Close()
	var node *cluster.Node
	isWriter := func() bool { return true }
	if clusterEnabled() {
		if node, err = startCluster(a); err != nil {
			return err
		}
		defer node.Close()
		isWriter = node.IsWriter
		waitReady(node, a)
	}
	if err := initSecret(a); err != nil {
		return err
	}
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
	if isWriter() {
		if err := bootstrap(a, srv); err != nil {
			return err
		}
		if err := a.svc.EdgeWriteSettings(); err != nil {
			slog.Error("写入内容分发设置失败", "err", err)
		}
	}
	go housekeeping(a, isWriter)
	bk := newBackup(a, isWriter)
	a.store.OnApply(bk.Changed)
	bkCtx, bkStop := context.WithCancel(context.Background())
	defer bkStop()
	go bk.Run(bkCtx)

	handler := srv.Handler()
	if node != nil {
		node.SetForwardHandler(handler)
		handler = forwardWrites(node, handler, func(r *http.Request) string {
			if a.cfg.TrustProxy {
				if xff := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); xff != "" {
					return xff
				}
			}
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			return host
		})
	}

	hs := &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           handler,
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

func housekeeping(a *app, isWriter func() bool) {
	t := time.NewTicker(time.Hour)
	runs := 0
	run := func() {
		w := isWriter()
		if w {
			a.store.PurgeExpiredSessions()
			a.store.TrimAudit(50000)
		}
		// 文件在 R2 上时，每小时重写一遍访问控制信息，兜底修复漏写的变更
		if w && a.svc.EdgeEnabled() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if _, err := a.svc.EdgeResync(ctx, false); err != nil {
				slog.Error("同步内容分发信息失败", "err", err)
			}
			cancel()
		}
		if n, freed, err := a.svc.GC(w); err == nil && n > 0 {
			slog.Info("清理无引用文件", "files", n, "freed", auth.FormatBytes(freed))
		}
		// 文件存本机时，每 6 小时检查一次是否完整，缺失的从其他机器取回
		if a.r2 == nil && runs%6 == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if n, err := a.svc.Repair(ctx); n > 0 || err != nil {
				slog.Info("补齐本机缺失的文件", "files", n, "err", err)
			}
			cancel()
		}
		runs++
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
	a, err := openCLI(true)
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
	a, err := openCLI(true)
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
	a, err := openCLI(true)
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

func cmdContentKey() error {
	a, err := openCLI(false)
	if err != nil {
		return err
	}
	defer a.store.Close()
	fmt.Println(a.signer.ContentKey())
	return nil
}

func cmdBackfillRender() error {
	a, err := openCLI(false)
	if err != nil {
		return err
	}
	defer a.store.Close()
	viewerFS, _ := fs.Sub(assets.FS, "web/viewer")
	srv := &httpapi.Server{Cfg: a.cfg, Store: a.store, Svc: a.svc, Signer: a.signer, ViewerFS: viewerFS, LibFS: a.libFS}
	if err := srv.Init(); err != nil {
		return err
	}
	n, err := a.svc.EdgeResync(context.Background(), true)
	fmt.Printf("已处理 %d 个 artifact\n", n)
	return err
}

func cmdGC() error {
	a, err := openCLI(true)
	if err != nil {
		return err
	}
	defer a.store.Close()
	n, freed, err := a.svc.GC(true)
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
	a, err := openCLI(false)
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
	fmt.Printf("数据库已备份到 %s。文件内容在 %s（或对象存储），请一并备份。\n", args[0], filepath.Join(a.cfg.DataDir, "blobs"))
	return nil
}

// cmdMigrateBlobs 把旧版本地布局（data/blobs/ab/cdef…，按 sha256 拆分）迁移到当前布局
// <artifact_id>/v<版本>/<路径>，目标是当前配置的存储（配置了 ARTIFACT_S3_* 就是对象存储）。
// 已存在的对象会跳过，可重复执行。加 --purge-legacy 会在迁移成功后删除目标存储里旧布局的对象。
func cmdMigrateBlobs(args []string) error {
	purge := len(args) > 0 && args[0] == "--purge-legacy"
	a, err := openCLI(true)
	if err != nil {
		return err
	}
	defer a.store.Close()
	legacy := filepath.Join(a.cfg.DataDir, "blobs")
	rows, err := a.store.DB.Query(`SELECT artifact_id,version,path,sha256 FROM version_files ORDER BY artifact_id,version,path`)
	if err != nil {
		return err
	}
	type item struct {
		loc blob.Loc
		sha string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.loc.ArtifactID, &it.loc.Version, &it.loc.Path, &it.sha); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	var uploaded, skipped, missing int
	for _, it := range items {
		if a.blobs.HasKey(it.loc.Key()) {
			skipped++
			continue
		}
		if len(it.sha) != 64 {
			missing++
			continue
		}
		data, err := os.ReadFile(filepath.Join(legacy, it.sha[:2], it.sha[2:]))
		if err != nil {
			fmt.Fprintf(os.Stderr, "缺失：%s（%s）\n", it.loc.Key(), err)
			missing++
			continue
		}
		if blob.Sum(data) != it.sha {
			fmt.Fprintf(os.Stderr, "校验失败：%s\n", it.loc.Key())
			missing++
			continue
		}
		if err := a.blobs.PutAt(it.loc, data); err != nil {
			return fmt.Errorf("上传 %s：%w", it.loc.Key(), err)
		}
		uploaded++
	}
	fmt.Printf("数据库里共 %d 个文件：新写入 %d，已存在 %d，失败 %d\n", len(items), uploaded, skipped, missing)
	if missing > 0 {
		return errors.New("有文件迁移失败，未清理旧对象")
	}
	if purge {
		keys, err := a.blobs.List()
		if err != nil {
			return err
		}
		n := 0
		for _, k := range keys {
			if blob.IsLegacyKey(k) {
				if err := a.blobs.DeleteKey(k); err != nil {
					return err
				}
				n++
			}
		}
		fmt.Printf("已删除目标存储里旧布局的对象 %d 个\n", n)
	}
	return nil
}
