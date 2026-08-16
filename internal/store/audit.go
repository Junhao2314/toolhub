package store

import (
	"context"
	"encoding/json"
	"net"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

type auditExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (s *Store) Audit(ctx context.Context, event domain.AuditEvent) error {
	return insertAudit(ctx, s.pool, event)
}

func insertAudit(ctx context.Context, execer auditExecer, event domain.AuditEvent) error {
	metadata, err := json.Marshal(security.RedactMap(event.Metadata))
	if err != nil {
		return err
	}
	var ip any
	if parsed := net.ParseIP(event.IPAddress); parsed != nil {
		ip = parsed.String()
	}
	_, err = execer.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,ip_address,metadata) VALUES($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), event.Action, event.ResourceType, event.ResourceID, event.Outcome, ip, jsonText(metadata))
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int) (json.RawMessage, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return s.JSONList(ctx, `SELECT id::text,action,resource_type AS "resourceType",resource_id AS "resourceId",outcome,host(ip_address) AS "ipAddress",metadata,created_at AS "createdAt" FROM audit_events ORDER BY created_at DESC LIMIT $1`, limit)
}
