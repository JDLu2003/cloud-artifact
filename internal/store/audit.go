package store

import "strings"

type AuditEntry struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	UserID   *int64 `json:"user_id"`
	UserName string `json:"user_name"`
	KeyID    *int64 `json:"key_id"`
	IP       string `json:"ip"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
}

func (s *Store) Audit(userID, keyID *int64, ip, action, target, detail string) {
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	s.DB.Exec(`INSERT INTO audit_log(ts,user_id,key_id,ip,action,target,detail) VALUES(?,?,?,?,?,?,?)`, now(), userID, keyID, ip, action, target, detail)
}

type AuditQuery struct {
	UserID int64
	Action string
	Target string
	Limit  int
	Offset int
}

func (s *Store) ListAudit(q AuditQuery) ([]AuditEntry, int64, error) {
	where := []string{"1=1"}
	args := []any{}
	if q.UserID > 0 {
		where = append(where, "l.user_id=?")
		args = append(args, q.UserID)
	}
	if q.Action != "" {
		where = append(where, "l.action LIKE ?")
		args = append(args, q.Action+"%")
	}
	if q.Target != "" {
		where = append(where, "l.target=?")
		args = append(args, q.Target)
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	w := " WHERE " + strings.Join(where, " AND ")
	var total int64
	s.DB.QueryRow(`SELECT COUNT(*) FROM audit_log l`+w, args...).Scan(&total)
	rows, err := s.DB.Query(`SELECT l.id,l.ts,l.user_id,COALESCE(u.name,''),l.key_id,l.ip,l.action,l.target,l.detail FROM audit_log l LEFT JOIN users u ON u.id=l.user_id`+w+` ORDER BY l.id DESC LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.UserID, &e.UserName, &e.KeyID, &e.IP, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// TrimAudit 只保留最近 keep 条审计日志。
func (s *Store) TrimAudit(keep int64) {
	s.DB.Exec(`DELETE FROM audit_log WHERE id <= (SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?)`, keep)
}
