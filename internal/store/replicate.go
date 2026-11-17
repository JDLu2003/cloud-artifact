package store

// 多机复制：所有写入都变成"语句批次"（SQL + 参数），提交给集群后在每台机器上按同样顺序执行。
// 本文件同时实现 cluster.FSM（Apply / Applied / Snapshot / Restore）。
//
// - 单条写入用 s.Exec；需要"先读后写"的多条写入用 s.Tx：在写节点上先用真实事务试执行（能读到本事务内
//   之前的写入），记录每条语句的影响行数后回滚，再把批次提交；各节点执行时若影响行数与试执行不同，
//   整批回滚并报冲突，保证不会出现部分写入。
// - 时间、ID 等都在 Go 里生成后作为参数传入，SQL 中不使用 datetime('now')、random() 等非确定性函数。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

// Applier 把命令提交到集群（*cluster.Node 满足此接口）。
type Applier interface {
	Apply(ctx context.Context, cmd []byte) ([]byte, error)
}

// Stmt 是批次中的一条语句。Expect >= 0 时要求影响行数等于它。
type Stmt struct {
	Q      string `json:"q"`
	Args   []Arg  `json:"a,omitempty"`
	Expect int64  `json:"e"`
}

// Arg 是带类型的参数（JSON 数字无法区分整数和浮点数）。
type Arg struct {
	I *int64   `json:"i,omitempty"`
	F *float64 `json:"f,omitempty"`
	S *string  `json:"s,omitempty"`
	B *string  `json:"b,omitempty"` // base64
	T *bool    `json:"t,omitempty"`
}

func encodeArgs(args []any) ([]Arg, error) {
	out := make([]Arg, len(args))
	for i, a := range args {
		v, err := driver.DefaultParameterConverter.ConvertValue(a)
		if err != nil {
			return nil, fmt.Errorf("参数 %d: %w", i, err)
		}
		switch x := v.(type) {
		case nil:
		case int64:
			out[i].I = &x
		case float64:
			out[i].F = &x
		case bool:
			out[i].T = &x
		case string:
			out[i].S = &x
		case []byte:
			s := base64.StdEncoding.EncodeToString(x)
			out[i].B = &s
		case time.Time:
			s := x.Format(time.RFC3339Nano)
			out[i].S = &s
		default:
			return nil, fmt.Errorf("参数 %d: 不支持的类型 %T", i, v)
		}
	}
	return out, nil
}

func decodeArgs(args []Arg) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch {
		case a.I != nil:
			out[i] = *a.I
		case a.F != nil:
			out[i] = *a.F
		case a.T != nil:
			out[i] = *a.T
		case a.S != nil:
			out[i] = *a.S
		case a.B != nil:
			b, _ := base64.StdEncoding.DecodeString(*a.B)
			out[i] = b
		}
	}
	return out
}

type batch struct {
	Stmts []Stmt `json:"stmts"`
}

// execResult 是批次中最后一条语句的结果。
type execResult struct {
	LastID int64 `json:"last_id"`
	Rows   int64 `json:"rows"`
}

func (r execResult) LastInsertId() (int64, error) { return r.LastID, nil }
func (r execResult) RowsAffected() (int64, error) { return r.Rows, nil }

// ErrConflict 表示批次在执行时与试执行的结果不一致（通常是并发修改）。
var ErrConflict = errors.New("数据已被并发修改，请重试")

const appliedKey = "cluster.applied"

// replication 是 Store 中与复制相关的状态。
type replication struct {
	ap      Applier
	writeMu sync.Mutex // 串行化写节点上的"试执行 + 提交"
	applyMu sync.Mutex // 串行化状态机执行
	applied uint64
	appMu   sync.RWMutex

	onApply func() // 每个批次执行成功后调用（例如触发备份）

	pendMu    sync.Mutex
	pendViews map[string]int64
	pendKeys  map[int64]keyTouch
}

type keyTouch struct {
	at int64
	ip string
}

// OnApply 设置批次执行成功后的回调（异步调用）。
func (s *Store) OnApply(fn func()) { s.rep.onApply = fn }

// SetApplier 设置集群提交入口；为空时直接在本地执行（单机、测试）。
func (s *Store) SetApplier(a Applier) {
	s.rep.writeMu.Lock()
	s.rep.ap = a
	s.rep.writeMu.Unlock()
}

func (s *Store) loadApplied() error {
	v, err := s.GetKV(appliedKey)
	if errors.Is(err, ErrNotFound) {
		s.setApplied(0)
		return nil
	}
	if err != nil {
		return err
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return fmt.Errorf("%s 无效: %w", appliedKey, err)
	}
	s.setApplied(n)
	return nil
}

func (s *Store) setApplied(n uint64) {
	s.rep.appMu.Lock()
	s.rep.applied = n
	s.rep.appMu.Unlock()
}

// Applied 返回已应用的位置（cluster.FSM）。
func (s *Store) Applied() uint64 {
	s.rep.appMu.RLock()
	defer s.rep.appMu.RUnlock()
	return s.rep.applied
}

// submit 把批次提交到集群（或本地执行）。
func (s *Store) submit(ctx context.Context, b batch) (execResult, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return execResult{}, err
	}
	ap := s.rep.ap
	var out []byte
	if ap == nil {
		out, err = s.Apply(s.Applied()+1, raw)
	} else {
		out, err = ap.Apply(ctx, raw)
	}
	if err != nil {
		if err.Error() == ErrConflict.Error() {
			return execResult{}, ErrConflict
		}
		return execResult{}, err
	}
	var r execResult
	_ = json.Unmarshal(out, &r)
	return r, nil
}

// Exec 提交一条写语句。
func (s *Store) Exec(q string, args ...any) (sql.Result, error) {
	a, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.submit(ctx, batch{Stmts: []Stmt{{Q: q, Args: a, Expect: -1}}})
}

// Tx 是试执行用的事务：写语句在真实事务里执行并被记录，读语句直接读事务内的状态。
type Tx struct {
	tx    *sql.Tx
	stmts []Stmt
}

func (t *Tx) Exec(q string, args ...any) (sql.Result, error) {
	a, err := encodeArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := t.tx.Exec(q, args...)
	if err != nil {
		return nil, err
	}
	n, _ := r.RowsAffected()
	t.stmts = append(t.stmts, Stmt{Q: q, Args: a, Expect: n})
	return r, nil
}

func (t *Tx) QueryRow(q string, args ...any) *sql.Row { return t.tx.QueryRow(q, args...) }

func (t *Tx) Query(q string, args ...any) (*sql.Rows, error) { return t.tx.Query(q, args...) }

// Tx 在写节点上试执行 fn，然后把记录下的语句作为一个批次提交。fn 返回错误时什么都不提交。
func (s *Store) Tx(ctx context.Context, fn func(*Tx) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.rep.writeMu.Lock()
	defer s.rep.writeMu.Unlock()
	real, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	t := &Tx{tx: real}
	err = fn(t)
	real.Rollback()
	if err != nil {
		return err
	}
	if len(t.stmts) == 0 {
		return nil
	}
	_, err = s.submit(ctx, batch{Stmts: t.stmts})
	return err
}

// Apply 执行一个批次（cluster.FSM）。无论成功与否都把已应用位置推进到 pos。
func (s *Store) Apply(pos uint64, raw []byte) ([]byte, error) {
	s.rep.applyMu.Lock()
	defer s.rep.applyMu.Unlock()
	defer s.invalidateSettings()
	var b batch
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, s.markApplied(pos, fmt.Errorf("命令格式错误: %w", err))
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, s.fatal(err)
	}
	var res execResult
	for _, st := range b.Stmts {
		r, err := tx.Exec(st.Q, decodeArgs(st.Args)...)
		if err != nil {
			tx.Rollback()
			return nil, s.markApplied(pos, err)
		}
		res.Rows, _ = r.RowsAffected()
		res.LastID, _ = r.LastInsertId()
		if st.Expect >= 0 && res.Rows != st.Expect {
			tx.Rollback()
			return nil, s.markApplied(pos, ErrConflict)
		}
	}
	if _, err := tx.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, appliedKey, strconv.FormatUint(pos, 10)); err != nil {
		tx.Rollback()
		return nil, s.fatal(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, s.fatal(err)
	}
	s.setApplied(pos)
	if fn := s.rep.onApply; fn != nil {
		go fn()
	}
	out, _ := json.Marshal(res)
	return out, nil
}

// markApplied 在批次失败时单独记录位置，并把失败原因作为结果返回。
func (s *Store) markApplied(pos uint64, cause error) error {
	if _, err := s.DB.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, appliedKey, strconv.FormatUint(pos, 10)); err != nil {
		return s.fatal(err)
	}
	s.setApplied(pos)
	return cause
}

// fatal：数据库本身写不进去（磁盘满等），继续运行会让副本分叉，直接退出由容器重启。
func (s *Store) fatal(err error) error {
	panic(fmt.Sprintf("数据库写入失败，无法继续复制: %v", err))
}

// Snapshot 输出整个数据库（cluster.FSM）。
func (s *Store) Snapshot(w io.Writer) error {
	dir := filepath.Dir(s.Path)
	tmp := filepath.Join(dir, fmt.Sprintf(".snapshot-%d.db", time.Now().UnixNano()))
	defer os.Remove(tmp)
	// VACUUM INTO 是一致性读，不阻塞写入；已应用位置在 kv 表里，随数据一起导出
	s.rep.applyMu.Lock()
	_, err := s.DB.Exec(`VACUUM INTO ?`, tmp)
	s.rep.applyMu.Unlock()
	if err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// Restore 用快照替换整个数据库（cluster.FSM）。用 SQLite 在线备份接口整体覆盖，连接池无需重建。
func (s *Store) Restore(r io.Reader) error {
	s.rep.applyMu.Lock()
	defer s.rep.applyMu.Unlock()
	defer s.invalidateSettings()
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, ".restore-*.db")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	conn, err := s.DB.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	err = conn.Raw(func(dc any) error {
		rc, ok := dc.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite 驱动不支持在线恢复")
		}
		b, err := rc.NewRestore(tmp.Name())
		if err != nil {
			return err
		}
		if _, err := b.Step(-1); err != nil {
			b.Finish()
			return err
		}
		return b.Finish()
	})
	if err != nil {
		return fmt.Errorf("恢复快照: %w", err)
	}
	if err := s.migrate(); err != nil {
		return err
	}
	return s.loadApplied()
}

// ---------- 高频小写入合并 ----------

// IncViews 累计浏览量，定期合并提交，避免每次浏览都产生一次集群写入。
func (s *Store) IncViews(id string) {
	s.rep.pendMu.Lock()
	if s.rep.pendViews == nil {
		s.rep.pendViews = map[string]int64{}
	}
	s.rep.pendViews[id]++
	s.rep.pendMu.Unlock()
}

// TouchKey 记录 Key 的最近使用时间和 IP（合并提交）。
func (s *Store) TouchKey(id int64, ip string) {
	s.rep.pendMu.Lock()
	if s.rep.pendKeys == nil {
		s.rep.pendKeys = map[int64]keyTouch{}
	}
	s.rep.pendKeys[id] = keyTouch{at: now(), ip: ip}
	s.rep.pendMu.Unlock()
}

// FlushPending 提交累计的浏览量和 Key 使用记录。失败时放回，下次再试。
func (s *Store) FlushPending(ctx context.Context) error {
	s.rep.pendMu.Lock()
	views, keys := s.rep.pendViews, s.rep.pendKeys
	s.rep.pendViews, s.rep.pendKeys = nil, nil
	s.rep.pendMu.Unlock()
	if len(views) == 0 && len(keys) == 0 {
		return nil
	}
	var b batch
	for id, n := range views {
		a, _ := encodeArgs([]any{n, id})
		b.Stmts = append(b.Stmts, Stmt{Q: `UPDATE artifacts SET views=views+? WHERE id=?`, Args: a, Expect: -1})
	}
	for id, t := range keys {
		a, _ := encodeArgs([]any{t.at, t.ip, id})
		b.Stmts = append(b.Stmts, Stmt{Q: `UPDATE api_keys SET last_used_at=?, last_ip=? WHERE id=?`, Args: a, Expect: -1})
	}
	if _, err := s.submit(ctx, b); err != nil {
		s.rep.pendMu.Lock()
		if s.rep.pendViews == nil {
			s.rep.pendViews = map[string]int64{}
		}
		for id, n := range views {
			s.rep.pendViews[id] += n
		}
		if s.rep.pendKeys == nil {
			s.rep.pendKeys = map[int64]keyTouch{}
		}
		for id, t := range keys {
			if _, ok := s.rep.pendKeys[id]; !ok {
				s.rep.pendKeys[id] = t
			}
		}
		s.rep.pendMu.Unlock()
		return err
	}
	return nil
}

func (s *Store) flushLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.FlushPending(fctx)
			cancel()
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			s.FlushPending(fctx)
			cancel()
		}
	}
}
