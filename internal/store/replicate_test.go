package store

import (
	"bytes"
	"context"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// applier 把命令同时执行到主库和副本，模拟集群复制。
type applier struct{ primary, replica *Store }

func (a *applier) Apply(_ context.Context, cmd []byte) ([]byte, error) {
	p := a.primary.Applied() + 1
	out, err := a.primary.Apply(p, cmd)
	_, _ = a.replica.Apply(p, cmd)
	return out, err
}

func TestBatchReplicationAndConflict(t *testing.T) {
	p, r := openTest(t), openTest(t)
	p.SetApplier(&applier{p, r})
	u, err := p.CreateUser(NewUser{Name: "alice", PasswordHash: "h", Role: "admin"})
	if err != nil || u.ID != 1 {
		t.Fatalf("CreateUser: %+v %v", u, err)
	}
	if ru, err := r.UserByName("alice"); err != nil || ru.ID != u.ID {
		t.Fatal("副本没有用户")
	}
	// 参数类型保持：整数仍是 INTEGER
	var typ string
	r.DB.QueryRow(`SELECT typeof(created_at) FROM users WHERE id=1`).Scan(&typ)
	if typ != "integer" {
		t.Fatalf("created_at 类型 %s", typ)
	}
	// Tx：试执行中的 RowsAffected 检查生效，副本上执行一致
	err = p.Tx(nil, func(tx *Tx) error {
		if err := InsertArtifact(tx, &Artifact{ID: "a1", OwnerID: 1, Title: "t", Kind: "html", Entry: "index.html", Visibility: "private"}); err != nil {
			return err
		}
		return UpsertFTS(tx, "a1", "t", "", "body")
	})
	if err != nil || !r.ArtifactExists("a1") {
		t.Fatalf("Tx 未复制: %v", err)
	}
	// 副本状态被"偷偷"改动后，带期望行数的批次在副本上整体失败而不是部分写入
	r.DB.Exec(`DELETE FROM artifacts WHERE id='a1'`)
	err = p.Tx(nil, func(tx *Tx) error {
		_, err := tx.Exec(`UPDATE artifacts SET title='x' WHERE id='a1'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied() != p.Applied() {
		t.Fatal("失败的批次也必须推进已应用位置")
	}
	// 浏览量合并提交
	p.IncViews("a1")
	p.IncViews("a1")
	if err := p.FlushPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, _ := p.ArtifactByID("a1")
	if a.Views != 2 {
		t.Fatalf("views = %d", a.Views)
	}
}

func TestSnapshotRestore(t *testing.T) {
	p, r := openTest(t), openTest(t)
	if _, err := p.CreateUser(NewUser{Name: "bob", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	st := p.Settings()
	st.MaxVersions = 7
	if err := p.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	_ = r.Settings() // 填充缓存，恢复后必须失效
	var buf bytes.Buffer
	if err := p.Snapshot(&buf); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UserByName("bob"); err != nil {
		t.Fatal("恢复后缺少用户")
	}
	if r.Applied() != p.Applied() || r.Settings().MaxVersions != 7 {
		t.Fatalf("applied %d/%d settings %d", r.Applied(), p.Applied(), r.Settings().MaxVersions)
	}
}
