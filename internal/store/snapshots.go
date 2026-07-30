package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/security"
)

func (s *Store) ResolveProfileManifest(ctx context.Context, profileID, targetID string) (bridgeprotocol.DesiredManifest, error) {
	profile, err := s.Profile(ctx, profileID)
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	target, err := s.Target(ctx, targetID)
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	if !target.Writable || target.Runtime == domain.RuntimeHermes {
		return bridgeprotocol.DesiredManifest{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrHermesReadOnly, Message: "Hermes targets are read-only"}
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: bridgeTarget(target), ProfileID: profile.ID, ProfileRevision: profile.Revision, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
	if target.Runtime != domain.RuntimeSharedRelay {
		rows, err := s.pool.Query(ctx, `SELECT sk.id::text,v.id::text,sk.slug,a.canonical_sha256,a.content_hash FROM profile_skills ps JOIN skills sk ON sk.id=ps.skill_id JOIN skill_versions v ON v.id=sk.current_version_id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE ps.profile_id=$1 AND sk.archived_at IS NULL ORDER BY sk.id`, profileID)
		if err != nil {
			return bridgeprotocol.DesiredManifest{}, err
		}
		for rows.Next() {
			var skillID, versionID, slug, sha, contentHash string
			if err := rows.Scan(&skillID, &versionID, &slug, &sha, &contentHash); err != nil {
				rows.Close()
				return bridgeprotocol.DesiredManifest{}, err
			}
			memberID := stableMemberID("skill", skillID)
			manifest.Skills = append(manifest.Skills, bridgeprotocol.SkillMember{MemberID: memberID, SkillID: skillID, VersionID: versionID, Slug: slug, SHA256: sha, ContentHash: contentHash})
			manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, memberID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return bridgeprotocol.DesiredManifest{}, err
		}
		rows.Close()
	}
	if target.Runtime == domain.RuntimeSharedRelay || target.NodeKind == bridgeprotocol.NodeKindSalt {
		rows, err := s.pool.Query(ctx, `SELECT ms.id::text,ms.revision,ms.name,ms.transport,ms.command,ms.args,ms.url,ms.env_refs,ms.header_refs,ms.content_hash FROM profile_mcp_servers pm JOIN mcp_servers ms ON ms.id=pm.server_id WHERE pm.profile_id=$1 ORDER BY ms.id`, profileID)
		if err != nil {
			return bridgeprotocol.DesiredManifest{}, err
		}
		for rows.Next() {
			var member bridgeprotocol.MCPMember
			if err := rows.Scan(&member.ServerID, &member.Revision, &member.Name, &member.Transport, &member.Command, &member.Args, &member.URL, &member.EnvRefs, &member.HeaderRefs, &member.ContentHash); err != nil {
				rows.Close()
				return bridgeprotocol.DesiredManifest{}, err
			}
			member.MemberID = stableMemberID("mcp", member.ServerID)
			manifest.MCPServers = append(manifest.MCPServers, member)
			manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, member.MemberID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return bridgeprotocol.DesiredManifest{}, err
		}
		rows.Close()
	}
	if target.Runtime == domain.RuntimeSharedRelay {
		manifest.RelayPort = settings.RelayPort
	}
	if err := manifest.Validate(true); err != nil {
		return bridgeprotocol.DesiredManifest{}, fmt.Errorf("resolve Profile manifest: %w", err)
	}
	return manifest, nil
}

func (s *Store) ResolveTargetManifest(ctx context.Context, targetID string, skillIDs, serverIDs []string) (bridgeprotocol.DesiredManifest, error) {
	target, err := s.Target(ctx, targetID)
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	if !target.Writable || target.Runtime == domain.RuntimeHermes {
		return bridgeprotocol.DesiredManifest{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrHermesReadOnly, Message: "Hermes targets are read-only"}
	}
	skillIDs, serverIDs = uniqueIDs(skillIDs), uniqueIDs(serverIDs)
	if len(skillIDs) > 500 || len(serverIDs) > 500 {
		return bridgeprotocol.DesiredManifest{}, errors.New("target membership exceeds safety limit")
	}
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: bridgeTarget(target), Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
	baseSkills := map[string]bridgeprotocol.SkillMember{}
	baseServers := map[string]bridgeprotocol.MCPMember{}
	if _, base, baseErr := s.ActiveDesiredManifest(ctx, targetID); baseErr == nil {
		for _, member := range base.Skills {
			baseSkills[member.SkillID] = member
		}
		for _, member := range base.MCPServers {
			baseServers[member.ServerID] = member
		}
	} else if !errors.Is(baseErr, ErrNotFound) {
		return bridgeprotocol.DesiredManifest{}, baseErr
	}
	if target.Runtime == domain.RuntimeSharedRelay && len(skillIDs) != 0 {
		return bridgeprotocol.DesiredManifest{}, errors.New("shared relay target cannot contain Skills")
	}
	if target.Runtime != domain.RuntimeSharedRelay {
		for _, skillID := range skillIDs {
			if uuid.Validate(skillID) != nil {
				return bridgeprotocol.DesiredManifest{}, ErrNotFound
			}
			if member, ok := baseSkills[skillID]; ok {
				manifest.Skills = append(manifest.Skills, member)
				manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, member.MemberID)
				continue
			}
			var member bridgeprotocol.SkillMember
			member.SkillID = skillID
			if err := s.pool.QueryRow(ctx, `SELECT v.id::text,sk.slug,a.canonical_sha256,a.content_hash FROM skills sk JOIN skill_versions v ON v.id=sk.current_version_id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE sk.id=$1 AND sk.archived_at IS NULL`, skillID).Scan(&member.VersionID, &member.Slug, &member.SHA256, &member.ContentHash); errors.Is(err, pgx.ErrNoRows) {
				return bridgeprotocol.DesiredManifest{}, ErrNotFound
			} else if err != nil {
				return bridgeprotocol.DesiredManifest{}, err
			}
			member.MemberID = stableMemberID("skill", skillID)
			manifest.Skills = append(manifest.Skills, member)
			manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, member.MemberID)
		}
	}
	if target.Runtime != domain.RuntimeSharedRelay && target.NodeKind != bridgeprotocol.NodeKindSalt && len(serverIDs) != 0 {
		return bridgeprotocol.DesiredManifest{}, errors.New("local MCP membership belongs to local/shared-relay")
	}
	if target.Runtime == domain.RuntimeSharedRelay || target.NodeKind == bridgeprotocol.NodeKindSalt {
		for _, serverID := range serverIDs {
			if uuid.Validate(serverID) != nil {
				return bridgeprotocol.DesiredManifest{}, ErrNotFound
			}
			if member, ok := baseServers[serverID]; ok {
				manifest.MCPServers = append(manifest.MCPServers, member)
				manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, member.MemberID)
				continue
			}
			var member bridgeprotocol.MCPMember
			if err := s.pool.QueryRow(ctx, `SELECT id::text,revision,name,transport,command,args,url,env_refs,header_refs,content_hash FROM mcp_servers WHERE id=$1`, serverID).Scan(&member.ServerID, &member.Revision, &member.Name, &member.Transport, &member.Command, &member.Args, &member.URL, &member.EnvRefs, &member.HeaderRefs, &member.ContentHash); errors.Is(err, pgx.ErrNoRows) {
				return bridgeprotocol.DesiredManifest{}, ErrNotFound
			} else if err != nil {
				return bridgeprotocol.DesiredManifest{}, err
			}
			member.MemberID = stableMemberID("mcp", member.ServerID)
			manifest.MCPServers = append(manifest.MCPServers, member)
			manifest.ManagedMemberIDs = append(manifest.ManagedMemberIDs, member.MemberID)
		}
	}
	if target.Runtime == domain.RuntimeSharedRelay {
		settings, err := s.Settings(ctx)
		if err != nil {
			return bridgeprotocol.DesiredManifest{}, err
		}
		manifest.RelayPort = settings.RelayPort
	}
	manifest.Normalize()
	if err := manifest.Validate(true); err != nil {
		return bridgeprotocol.DesiredManifest{}, fmt.Errorf("resolve target manifest: %w", err)
	}
	return manifest, nil
}

func stableMemberID(kind, id string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(kind+":"+id)).String()
}

func ManifestSecretIDs(manifest bridgeprotocol.DesiredManifest) []string {
	seen := map[string]bool{}
	for _, server := range manifest.MCPServers {
		for _, refs := range []map[string]string{server.EnvRefs, server.HeaderRefs} {
			for _, id := range refs {
				seen[id] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func (s *Store) DesiredManifestOrEmpty(ctx context.Context, targetID string) (bridgeprotocol.DesiredManifest, error) {
	_, manifest, err := s.ActiveDesiredManifest(ctx, targetID)
	if err == nil {
		return manifest, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return bridgeprotocol.DesiredManifest{}, err
	}
	target, err := s.Target(ctx, targetID)
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	manifest = bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersion,
		Target:           bridgeTarget(target),
		Skills:           []bridgeprotocol.SkillMember{},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{},
	}
	if target.Runtime == domain.RuntimeSharedRelay {
		settings, err := s.Settings(ctx)
		if err != nil {
			return bridgeprotocol.DesiredManifest{}, err
		}
		manifest.RelayPort = settings.RelayPort
	}
	return manifest, manifest.Validate(true)
}

func (s *Store) ValidateManifestReferences(ctx context.Context, targetID string, manifest bridgeprotocol.DesiredManifest) error {
	target, err := s.Target(ctx, targetID)
	if err != nil {
		return err
	}
	manifest.ProfileID = ""
	manifest.ProfileRevision = 0
	if !reflect.DeepEqual(manifest.Target, bridgeTarget(target)) {
		return errors.New("manifest target does not match the current target")
	}
	if target.Runtime == domain.RuntimeSharedRelay {
		settings, err := s.Settings(ctx)
		if err != nil {
			return err
		}
		if manifest.RelayPort != settings.RelayPort {
			return errors.New("manifest relay port does not match Settings")
		}
	}
	if err := manifest.Validate(true); err != nil {
		return err
	}
	for _, member := range manifest.Skills {
		var skillID, slug, sha, contentHash string
		err := s.pool.QueryRow(ctx, `SELECT v.skill_id::text,sk.slug,a.canonical_sha256,a.content_hash FROM skill_versions v JOIN skills sk ON sk.id=v.skill_id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE v.id=$1 AND sk.archived_at IS NULL`, member.VersionID).Scan(&skillID, &slug, &sha, &contentHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if skillID != member.SkillID || slug != member.Slug || sha != member.SHA256 || contentHash != member.ContentHash || member.MemberID != stableMemberID("skill", skillID) {
			return errors.New("manifest contains a mismatched Skill reference")
		}
	}
	for _, member := range manifest.MCPServers {
		var current bridgeprotocol.MCPMember
		if err := s.pool.QueryRow(ctx, `SELECT id::text,revision,name,transport,command,args,url,env_refs,header_refs,content_hash FROM mcp_servers WHERE id=$1`, member.ServerID).Scan(&current.ServerID, &current.Revision, &current.Name, &current.Transport, &current.Command, &current.Args, &current.URL, &current.EnvRefs, &current.HeaderRefs, &current.ContentHash); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		current.MemberID = stableMemberID("mcp", current.ServerID)
		if !reflect.DeepEqual(current, member) {
			return errors.New("manifest contains a mismatched MCP revision or secret reference")
		}
	}
	return nil
}

func (s *Store) CreatePreflightConfirmation(ctx context.Context, profileID, targetID, targetRevision string, manifest bridgeprotocol.DesiredManifest, diff bridgeprotocol.Diff, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > 10*time.Minute {
		return "", time.Time{}, errors.New("preflight confirmation TTL is invalid")
	}
	body, hash, err := manifest.Canonical()
	if err != nil {
		return "", time.Time{}, err
	}
	encodedDiff, err := json.Marshal(diff)
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(ttl)
	_, err = s.pool.Exec(ctx, `INSERT INTO preflight_confirmations(token_hash,profile_id,profile_revision,target_id,target_revision,manifest_hash,manifest,diff,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, security.TokenHash(token), profileID, manifest.ProfileRevision, targetID, targetRevision, hash, jsonText(body), jsonText(encodedDiff), expires)
	return token, expires, err
}

type ConfirmedPreflight struct {
	ProfileID       string
	ProfileRevision int64
	TargetID        string
	TargetRevision  string
	ManifestHash    string
	Manifest        bridgeprotocol.DesiredManifest
	Diff            bridgeprotocol.Diff
}

func (s *Store) CreateProfileApplyOperation(ctx context.Context, profileID string, tokens []string, idempotencyKey string) (domain.Operation, error) {
	if uuid.Validate(profileID) != nil || len(tokens) == 0 || len(tokens) > 100 {
		return domain.Operation{}, errors.New("Profile Apply confirmation set is invalid")
	}
	tokenHashes := make([]string, 0, len(tokens))
	seenTokens := map[string]bool{}
	for _, token := range tokens {
		hash := fmt.Sprintf("%x", security.TokenHash(strings.TrimSpace(token)))
		if strings.TrimSpace(token) == "" || seenTokens[hash] {
			return domain.Operation{}, ErrConflict
		}
		seenTokens[hash] = true
		tokenHashes = append(tokenHashes, hash)
	}
	sort.Strings(tokenHashes)
	request := map[string]any{"profileId": profileID, "confirmationTokenHashes": tokenHashes}
	requestHash, err := operationRequestHash(request, nil)
	if err != nil {
		return domain.Operation{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if idempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM operations WHERE kind='apply' AND idempotency_key=$1`, idempotencyKey).Scan(&existingID, &existingHash)
		if err == nil {
			if existingHash != requestHash {
				return domain.Operation{}, ErrIdempotencyConflict
			}
			_ = tx.Rollback(ctx)
			return s.Operation(ctx, existingID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.Operation{}, err
		}
	}
	var currentProfileRevision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM profiles WHERE id=$1 FOR SHARE`, profileID).Scan(&currentProfileRevision); errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	} else if err != nil {
		return domain.Operation{}, err
	}
	confirmed := make([]ConfirmedPreflight, 0, len(tokens))
	seenTargets := map[string]bool{}
	for _, token := range tokens {
		var item ConfirmedPreflight
		var manifestJSON, diffJSON []byte
		err := tx.QueryRow(ctx, `SELECT profile_id::text,profile_revision,target_id::text,target_revision,manifest_hash,manifest,diff FROM preflight_confirmations WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, security.TokenHash(strings.TrimSpace(token))).Scan(&item.ProfileID, &item.ProfileRevision, &item.TargetID, &item.TargetRevision, &item.ManifestHash, &manifestJSON, &diffJSON)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Operation{}, ErrConflict
		}
		if err != nil {
			return domain.Operation{}, err
		}
		if item.ProfileID != profileID || item.ProfileRevision != currentProfileRevision || seenTargets[item.TargetID] {
			return domain.Operation{}, ErrConflict
		}
		item.Manifest, err = bridgeprotocol.DecodeManifest(manifestJSON, true)
		if err != nil || item.Manifest.Target.ID != item.TargetID || item.Manifest.ProfileID != profileID || item.Manifest.ProfileRevision != currentProfileRevision {
			return domain.Operation{}, ErrConflict
		}
		_, canonicalHash, err := item.Manifest.Canonical()
		if err != nil || canonicalHash != item.ManifestHash {
			return domain.Operation{}, ErrConflict
		}
		if err := json.Unmarshal(diffJSON, &item.Diff); err != nil {
			return domain.Operation{}, err
		}
		seenTargets[item.TargetID] = true
		confirmed = append(confirmed, item)
	}
	operationID := uuid.NewString()
	metadata, _ := json.Marshal(map[string]any{"profileId": profileID, "targetCount": len(confirmed)})
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,idempotency_key,request_hash,metadata) VALUES($1,'apply','queued',$2,$3,$4,$5)`, operationID, profileID, nullableText(idempotencyKey), requestHash, jsonText(metadata)); err != nil {
		return domain.Operation{}, err
	}
	for _, item := range confirmed {
		targetRequest, err := json.Marshal(map[string]any{"manifest": item.Manifest, "targetRevision": item.TargetRevision, "sourceKind": "profile_apply", "sourceId": profileID})
		if err != nil {
			return domain.Operation{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,request) VALUES($1,$2,$3,$4)`, uuid.NewString(), operationID, item.TargetID, jsonText(targetRequest)); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.Operation{}, ErrOperationActive
			}
			return domain.Operation{}, err
		}
	}
	for _, token := range tokens {
		if _, err := tx.Exec(ctx, `UPDATE preflight_confirmations SET consumed_at=now() WHERE token_hash=$1`, security.TokenHash(strings.TrimSpace(token))); err != nil {
			return domain.Operation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, operationID)
}

func (s *Store) ConsumePreflightConfirmation(ctx context.Context, token string) (ConfirmedPreflight, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ConfirmedPreflight{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var confirmed ConfirmedPreflight
	var manifestJSON, diffJSON []byte
	err = tx.QueryRow(ctx, `SELECT profile_id::text,profile_revision,target_id::text,target_revision,manifest_hash,manifest,diff FROM preflight_confirmations WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, security.TokenHash(token)).Scan(&confirmed.ProfileID, &confirmed.ProfileRevision, &confirmed.TargetID, &confirmed.TargetRevision, &confirmed.ManifestHash, &manifestJSON, &diffJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfirmedPreflight{}, ErrConflict
	}
	if err != nil {
		return ConfirmedPreflight{}, err
	}
	manifest, err := bridgeprotocol.DecodeManifest(manifestJSON, true)
	if err != nil {
		return ConfirmedPreflight{}, err
	}
	confirmed.Manifest = manifest
	if err := json.Unmarshal(diffJSON, &confirmed.Diff); err != nil {
		return ConfirmedPreflight{}, err
	}
	var currentRevision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM profiles WHERE id=$1`, confirmed.ProfileID).Scan(&currentRevision); err != nil || currentRevision != confirmed.ProfileRevision {
		return ConfirmedPreflight{}, ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE preflight_confirmations SET consumed_at=now() WHERE token_hash=$1`, security.TokenHash(token)); err != nil {
		return ConfirmedPreflight{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfirmedPreflight{}, err
	}
	return confirmed, nil
}

func (s *Store) PinDesiredSnapshot(ctx context.Context, targetID, sourceKind, sourceID, operationTargetID string, manifest bridgeprotocol.DesiredManifest) (domain.DesiredSnapshot, error) {
	body, hash, err := manifest.Canonical()
	if err != nil {
		return domain.DesiredSnapshot{}, err
	}
	if manifest.Target.ID != targetID {
		return domain.DesiredSnapshot{}, errors.New("manifest target does not match snapshot target")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DesiredSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operationTargetValue any
	if uuid.Validate(operationTargetID) == nil {
		operationTargetValue = operationTargetID
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM desired_snapshots WHERE source_operation_target_id=$1`, operationTargetID).Scan(&existingID)
		if err == nil {
			_ = tx.Rollback(ctx)
			return s.DesiredSnapshot(ctx, existingID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.DesiredSnapshot{}, err
		}
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0)+1 FROM desired_snapshots WHERE target_id=$1`, targetID).Scan(&revision); err != nil {
		return domain.DesiredSnapshot{}, err
	}
	id := uuid.NewString()
	var sourceValue any
	if uuid.Validate(sourceID) == nil {
		sourceValue = sourceID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,source_id,source_operation_target_id,profile_revision,manifest_schema_version,manifest_hash,manifest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, targetID, revision, sourceKind, sourceValue, operationTargetValue, manifest.ProfileRevision, manifest.SchemaVersion, hash, jsonText(body)); err != nil {
		return domain.DesiredSnapshot{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO target_desired_snapshots(target_id,snapshot_id,desired_revision,health,drift_summary,error_code,error_reason,updated_at) VALUES($1,$2,$3,'healthy','{}','','',now()) ON CONFLICT(target_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,desired_revision=EXCLUDED.desired_revision,health='healthy',drift_summary='{}',error_code='',error_reason='',updated_at=now()`, targetID, id, revision); err != nil {
		return domain.DesiredSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DesiredSnapshot{}, err
	}
	return s.DesiredSnapshot(ctx, id)
}

func (s *Store) DesiredSnapshot(ctx context.Context, id string) (domain.DesiredSnapshot, error) {
	var snapshot domain.DesiredSnapshot
	err := s.pool.QueryRow(ctx, `SELECT id::text,target_id::text,revision,source_kind,coalesce(source_id::text,''),coalesce(profile_revision,0),manifest_schema_version,manifest_hash,manifest,created_at FROM desired_snapshots WHERE id=$1`, id).Scan(&snapshot.ID, &snapshot.TargetID, &snapshot.Revision, &snapshot.SourceKind, &snapshot.SourceID, &snapshot.ProfileRevision, &snapshot.ManifestSchemaVersion, &snapshot.ManifestHash, &snapshot.Manifest, &snapshot.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DesiredSnapshot{}, ErrNotFound
	}
	return snapshot, err
}

func (s *Store) ActiveDesiredManifest(ctx context.Context, targetID string) (domain.DesiredSnapshot, bridgeprotocol.DesiredManifest, error) {
	var snapshotID string
	if err := s.pool.QueryRow(ctx, `SELECT snapshot_id::text FROM target_desired_snapshots WHERE target_id=$1`, targetID).Scan(&snapshotID); errors.Is(err, pgx.ErrNoRows) {
		return domain.DesiredSnapshot{}, bridgeprotocol.DesiredManifest{}, ErrNotFound
	} else if err != nil {
		return domain.DesiredSnapshot{}, bridgeprotocol.DesiredManifest{}, err
	}
	snapshot, err := s.DesiredSnapshot(ctx, snapshotID)
	if err != nil {
		return domain.DesiredSnapshot{}, bridgeprotocol.DesiredManifest{}, err
	}
	manifest, err := bridgeprotocol.DecodeManifest(snapshot.Manifest, true)
	return snapshot, manifest, err
}

func (s *Store) UpdateTargetHealth(ctx context.Context, targetID, health, errorCode, errorReason string, drift any, repaired bool) (bool, error) {
	errorReason = truncate(errorReason, 500)
	driftJSON, err := json.Marshal(drift)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldHealth, oldCode, oldReason string
	if err := tx.QueryRow(ctx, `SELECT health,error_code,error_reason FROM target_desired_snapshots WHERE target_id=$1 FOR UPDATE`, targetID).Scan(&oldHealth, &oldCode, &oldReason); err != nil {
		return false, err
	}
	changed := oldHealth != health || oldCode != errorCode || oldReason != errorReason
	if _, err := tx.Exec(ctx, `UPDATE target_desired_snapshots SET health=$2,error_code=$3,error_reason=$4,drift_summary=$5,last_reconciled_at=now(),last_repair_at=CASE WHEN $6 THEN now() ELSE last_repair_at END,updated_at=now() WHERE target_id=$1`, targetID, health, errorCode, truncate(errorReason, 500), jsonText(driftJSON), repaired); err != nil {
		return false, err
	}
	if changed {
		if _, err := tx.Exec(ctx, `UPDATE alerts SET acknowledged_at=now() WHERE target_id=$1 AND acknowledged_at IS NULL`, targetID); err != nil {
			return false, err
		}
		if health != bridgeprotocol.HealthHealthy {
			alertCode, message := errorCode, errorReason
			if alertCode == "" {
				alertCode = "target_" + health
			}
			if message == "" {
				message = "Target health changed to " + health
			}
			if _, err := tx.Exec(ctx, `INSERT INTO alerts(id,target_id,severity,code,message) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), targetID, "warning", alertCode, message); err != nil {
				return false, err
			}
		}
		metadata, _ := json.Marshal(map[string]any{"previousHealth": oldHealth, "health": health, "previousErrorCode": oldCode, "errorCode": errorCode})
		outcome := "failure"
		if health == bridgeprotocol.HealthHealthy {
			outcome = "success"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'target_health_changed','target',$2,$3,$4)`, uuid.NewString(), targetID, outcome, jsonText(metadata)); err != nil {
			return false, err
		}
	}
	return changed, tx.Commit(ctx)
}

func (s *Store) RecordBackup(ctx context.Context, backup bridgeprotocol.Backup, operationID string, desiredManifest *bridgeprotocol.DesiredManifest) (domain.Backup, error) {
	id := uuid.NewString()
	expires := backup.CreatedAt.Add(30 * 24 * time.Hour)
	manifestHash := ""
	metadata := map[string]any{}
	if desiredManifest != nil {
		body, hash, err := desiredManifest.Canonical()
		if err != nil {
			return domain.Backup{}, err
		}
		manifestHash = hash
		metadata["desiredManifest"] = json.RawMessage(body)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return domain.Backup{}, err
	}
	var operationValue any
	if uuid.Validate(operationID) == nil {
		operationValue = operationID
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO backups(id,bridge_backup_id,target_id,source_operation_id,target_revision,manifest_hash,created_at,expires_at,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(bridge_backup_id) DO NOTHING`, id, backup.ID, backup.TargetID, operationValue, backup.Revision, nullableHash(manifestHash), backup.CreatedAt, expires, jsonText(metadataJSON))
	if err != nil {
		return domain.Backup{}, err
	}
	return s.BackupByBridgeID(ctx, backup.ID)
}

func nullableHash(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Backup(ctx context.Context, id string) (domain.Backup, error) {
	var backup domain.Backup
	err := s.pool.QueryRow(ctx, `SELECT id::text,bridge_backup_id,target_id::text,coalesce(source_operation_id::text,''),target_revision,coalesce(manifest_hash,''),created_at,expires_at,metadata FROM backups WHERE id=$1`, id).Scan(&backup.ID, &backup.BridgeBackupID, &backup.TargetID, &backup.SourceOperationID, &backup.TargetRevision, &backup.ManifestHash, &backup.CreatedAt, &backup.ExpiresAt, &backup.Metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Backup{}, ErrNotFound
	}
	return backup, err
}

func (s *Store) BackupByBridgeID(ctx context.Context, bridgeID string) (domain.Backup, error) {
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM backups WHERE bridge_backup_id=$1`, bridgeID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return domain.Backup{}, ErrNotFound
	} else if err != nil {
		return domain.Backup{}, err
	}
	return s.Backup(ctx, id)
}

func (s *Store) BackupDesiredManifest(ctx context.Context, backup domain.Backup) (bridgeprotocol.DesiredManifest, error) {
	var metadata struct {
		DesiredManifest json.RawMessage `json:"desiredManifest"`
	}
	if err := json.Unmarshal(backup.Metadata, &metadata); err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	if len(metadata.DesiredManifest) == 0 {
		return bridgeprotocol.DesiredManifest{}, errors.New("backup does not contain a pinned desired manifest")
	}
	return bridgeprotocol.DecodeManifest(metadata.DesiredManifest, true)
}

func (s *Store) ListBackups(ctx context.Context, targetID string) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text,bridge_backup_id AS "bridgeBackupId",target_id::text AS "targetId",coalesce(source_operation_id::text,'') AS "sourceOperationId",target_revision AS "targetRevision",coalesce(manifest_hash,'') AS "manifestHash",created_at AS "createdAt",expires_at AS "expiresAt",metadata FROM backups WHERE target_id=$1 ORDER BY created_at DESC`, targetID)
}

func (s *Store) DeleteBackupsByBridgeIDs(ctx context.Context, bridgeIDs []string) (int64, error) {
	if len(bridgeIDs) == 0 {
		return 0, nil
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM backups WHERE bridge_backup_id=ANY($1::text[])`, bridgeIDs)
	return command.RowsAffected(), err
}
