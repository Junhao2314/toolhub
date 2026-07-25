package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/toolhub-dev/toolhub/internal/domain"
	"github.com/toolhub-dev/toolhub/internal/security"
)

func (s *Store) Audit(ctx context.Context, event domain.AuditEvent) error {
	metadata, err := json.Marshal(security.RedactMap(event.Metadata))
	if err != nil {
		metadata = []byte("{}")
	}
	var actor any
	if event.ActorUserID != "" {
		actor = event.ActorUserID
	}
	var ip any
	if event.IPAddress != "" {
		ip = event.IPAddress
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_events(id,actor_user_id,action,resource_type,resource_id,outcome,ip_address,metadata)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.NewString(), actor, event.Action, event.ResourceType, event.ResourceID, event.Outcome, ip, metadata)
	return err
}
