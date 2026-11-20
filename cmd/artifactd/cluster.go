package main

// 多机部署：由 VPS 面板（vpnmgr）的 agent 写入以下环境变量后启动——
//   NODE_ID        机器 ID（与该机器上的 vpnmgr 相同）
//   PUBLIC_HOST    本机公网地址
//   CLUSTER_PORT   artifact 集群端口（默认 7947）
//   CLUSTER_SEED   集群种子（与 vpnmgr 相同，派生出 artifact 自己的证书）
//   CLUSTER_PEERS  其他运行 artifact 的机器的集群地址，逗号分隔
// 未设置 CLUSTER_SEED 时按单机运行，行为与以前一致。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JDLu2003/vps-cluster"
	"github.com/JDLu2003/vps-cluster/backup"
	"github.com/JDLu2003/vps-cluster/s3"

	"vps-artifact/internal/blob"
)

func clusterEnabled() bool { return strings.TrimSpace(os.Getenv("CLUSTER_SEED")) != "" }

func startCluster(a *app) (*cluster.Node, error) {
	host := strings.TrimSpace(os.Getenv("PUBLIC_HOST"))
	port := strings.TrimSpace(os.Getenv("CLUSTER_PORT"))
	if port == "" {
		port = "7947"
	}
	if host == "" {
		return nil, errors.New("集群模式需要 PUBLIC_HOST")
	}
	var peers []string
	for _, p := range strings.Split(os.Getenv("CLUSTER_PEERS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			peers = append(peers, p)
		}
	}
	n, err := cluster.Start(cluster.Config{
		Service: "artifact", NodeID: strings.TrimSpace(os.Getenv("NODE_ID")),
		Advertise: net.JoinHostPort(host, port), Listen: ":" + port,
		DataDir: filepath.Join(a.cfg.DataDir, "cluster"), FSM: a.store,
		Seed: strings.TrimSpace(os.Getenv("CLUSTER_SEED")), Peers: peers,
		Logf: func(format string, args ...any) { slog.Info(fmt.Sprintf("[集群] "+format, args...)) },
	})
	if err != nil {
		return nil, err
	}
	a.store.SetApplier(n)
	a.svc.GCMinAge = time.Hour
	if a.r2 != nil {
		return n, nil // 文件在 R2 上，所有机器共用，无需在机器之间同步
	}

	// 文件存本机时：其他机器拉取/推送文件的内部接口（只有持有集群身份的节点能访问）
	n.Handle("GET /blob/{key...}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := a.blobs.ReadLocal(r.PathValue("key"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	n.Handle("PUT /blob/{key...}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if !blob.IsArtifactKey(key) {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 128<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.blobs.PutKey(key, b); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	a.blobs.SetFetcher(func(key string) ([]byte, error) { return fetchBlob(a, n, key) })
	a.svc.Replicate = func(ctx context.Context, keys []string) error { return pushBlobs(ctx, a, n, keys) }
	return n, nil
}

// peers 返回除本机外的成员地址，写节点排在最前。
func peers(n *cluster.Node) []string {
	in := n.Info()
	var out []string
	for _, m := range in.Members {
		if m.ID == in.NodeID {
			continue
		}
		if m.ID == in.Writer {
			out = append([]string{m.Addr}, out...)
		} else {
			out = append(out, m.Addr)
		}
	}
	return out
}

// fetchBlob 依次从其他机器取回文件。
func fetchBlob(a *app, n *cluster.Node, key string) ([]byte, error) {
	for _, addr := range peers(n) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/_app/blob/"+key, nil)
		resp, err := n.HTTPClient().Do(req)
		if err == nil {
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
			resp.Body.Close()
			cancel()
			if resp.StatusCode == http.StatusOK && rerr == nil {
				return b, nil
			}
			continue
		}
		cancel()
	}
	return nil, os.ErrNotExist
}

// pushBlobs 把新写入的文件推送给其他机器。Raft 模式要求多数机器（含本机）收到；
// 主备模式尽力推送，备设备失联时仍允许发布（它恢复后会自动补齐）。
func pushBlobs(ctx context.Context, a *app, n *cluster.Node, keys []string) error {
	in := n.Info()
	others := peers(n)
	if len(others) == 0 || len(keys) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var mu sync.Mutex
	ok := 1 // 本机
	var errs []string
	var wg sync.WaitGroup
	for _, addr := range others {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			for _, k := range keys {
				b, err := a.blobs.ReadLocal(k)
				if err != nil {
					mu.Lock()
					errs = append(errs, err.Error())
					mu.Unlock()
					return
				}
				req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "https://"+addr+"/_app/blob/"+k, bytes.NewReader(b))
				resp, err := n.HTTPClient().Do(req)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode/100 != 2 {
						err = fmt.Errorf("HTTP %d", resp.StatusCode)
					}
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, addr+": "+err.Error())
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			ok++
			mu.Unlock()
		}(addr)
	}
	wg.Wait()
	total := len(others) + 1
	if in.Mode == cluster.ModeRaft && ok < total/2+1 {
		return fmt.Errorf("只有 %d/%d 台机器收到文件: %s", ok, total, strings.Join(errs, "; "))
	}
	if len(errs) > 0 {
		slog.Warn("部分机器未收到文件，稍后会自动补齐", "errors", strings.Join(errs, "; "))
	}
	return nil
}

// waitReady 等到本机可以安全读取复制状态：要么是写节点，要么已经同步到数据。
func waitReady(n *cluster.Node, a *app) {
	for i := 0; ; i++ {
		in := n.Info()
		if in.Role != "joining" && (n.IsWriter() || a.store.Applied() > 0) {
			return
		}
		if i%30 == 0 {
			slog.Info("等待从集群同步数据…", "role", in.Role)
		}
		time.Sleep(time.Second)
	}
}

func r2Config(a *app) (s3.Config, bool) {
	c := a.cfg
	cfg := s3.Config{Endpoint: c.S3Endpoint, Bucket: c.S3Bucket, AccessKey: c.S3AccessKey, SecretKey: c.S3SecretKey, Region: c.S3Region, Prefix: c.S3Prefix}
	return cfg, cfg.Valid()
}

// newBackup 返回数据库快照备份任务（文件本身已即时写入 R2，不需要另外备份）。
// 有写入就在 1 分钟内备份，另外每 6 小时兜底一次；快照按小时保留 48 份。
func newBackup(a *app, isWriter func() bool) *backup.Runner {
	return &backup.Runner{
		Service: "artifact", Source: a.store, IsWriter: isWriter,
		Debounce: time.Minute, Keep: 48,
		Config: func() (s3.Config, bool) { return r2Config(a) },
	}
}

// forwardWrites 把修改类请求转给写节点执行。
func forwardWrites(n *cluster.Node, h http.Handler, clientIP func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions &&
			!cluster.Forwarded(r) && !n.IsWriter() {
			n.ProxyToWriter(w, r, clientIP(r))
			return
		}
		h.ServeHTTP(w, r)
	})
}
