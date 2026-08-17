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
	if profile.ArchivedAt != nil || profile.PendingBindings {
		return bridgeprotocol.DesiredManifest{}, ErrConflict
	}
	manifest := bridgeprotocol.DesiredManifest{SchemaVersion: bridgeprotocol.ManifestSchemaVersion, Target: bridgeTarget(target), ProfileID: profile.ID, ProfileRevision: profile.Revision, Skills: []bridgeprotocol.SkillMember{}, MCPServers: []bridgeprotocol.MCPMember{}, ManagedMemberIDs: []string{}}
	if target.Runtime != domain.RuntimeSharedRelay {
		rows, err := s.pool.Query(ctx, `SELECT prs.skill_id::text,prs.skill_version_id::text,sk.slug,a.canonical_sha256,a.content_hash FROM profiles p JOIN profile_revision_skills prs ON prs.profile_revision_id=p.current_revision_id JOIN skills sk ON sk.id=prs.skill_id JOIN skill_versions v ON v.id=prs.skill_version_id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE p.id=$1 ORDER BY prs.position`, profileID)
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
	if target.Runtime == domain.RuntimeSharedRelay {
		candidate := RoutingBundleCandidate{PublishedProfileRevisions: map[string]string{profile.ID: profile.CurrentRevisionID}}
		return s.resolveRelayManifestCandidate(ctx, target, profile.ID, profile.Revision, candidate)
	}
	// Profile manifests never carry MCP membership. Remote MCP delivery, when
	// explicitly required, is owned by the target/relay configuration workflow
	// rather than by Profile revisions.
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
		if err := s.attachRelayGovernance(ctx, &manifest); err != nil {
			return bridgeprotocol.DesiredManifest{}, err
		}
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
		if err := s.attachRelayGovernance(ctx, &manifest); err != nil {
			return bridgeprotocol.DesiredManifest{}, err
		}
	}
	return manifest, manifest.Validate(true)
}

func (s *Store) attachRelayGovernance(ctx context.Context, manifest *bridgeprotocol.DesiredManifest) error {
	bundle, hash, err := s.RenderRelayConfigurationBundle(ctx, RoutingBundleCandidate{})
	if err != nil {
		return err
	}
	return attachRenderedRelayGovernance(manifest, bundle, hash)
}

func attachRenderedRelayGovernance(manifest *bridgeprotocol.DesiredManifest, bundle RoutingBundle, hash string) error {
	body, canonicalHash, err := bundle.Canonical()
	if err != nil {
		return err
	}
	if canonicalHash != hash {
		return errors.New("routing bundle hash changed during render")
	}
	manifest.SchemaVersion = bridgeprotocol.ManifestSchemaVersionV2
	manifest.RelayGovernance = &bridgeprotocol.RelayGovernanceManifest{
		RelayConfigurationRevisionID: bundle.RelayConfigurationRevisionID,
		RelayConfigurationHash:       bundle.RelayConfigurationHash,
		RoutingBundle:                json.RawMessage(body),
		RoutingHash:                  hash,
	}
	return nil
}

func (s *Store) resolveRelayManifestCandidate(ctx context.Context, target domain.Target, profileID string, profileRevision int64, candidate RoutingBundleCandidate) (bridgeprotocol.DesiredManifest, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	manifest, err := s.resolveRelayManifestCandidateTx(ctx, tx, target, profileID, profileRevision, candidate)
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	return manifest, nil
}

func (s *Store) resolveRelayManifestCandidateTx(ctx context.Context, tx pgx.Tx, target domain.Target, profileID string, profileRevision int64, candidate RoutingBundleCandidate) (bridgeprotocol.DesiredManifest, error) {
	var bundle RoutingBundle
	var routingHash string
	var err error
	if len(candidate.PublishedProfileRevisions) == 0 && !candidate.ReplacePublishedProfiles {
		bundle, routingHash, err = s.RenderRelayConfigurationBundleTx(ctx, tx, candidate)
	} else {
		bundle, routingHash, err = s.RenderCandidateRoutingBundleTx(ctx, tx, candidate)
	}
	if err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	var relayPort int
	if err := tx.QueryRow(ctx, `SELECT relay_port FROM settings WHERE singleton`).Scan(&relayPort); err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	manifest := bridgeprotocol.DesiredManifest{
		SchemaVersion:    bridgeprotocol.ManifestSchemaVersionV2,
		Target:           bridgeTarget(target),
		ProfileID:        profileID,
		ProfileRevision:  profileRevision,
		Skills:           []bridgeprotocol.SkillMember{},
		MCPServers:       []bridgeprotocol.MCPMember{},
		ManagedMemberIDs: []string{},
		RelayPort:        relayPort,
	}
	rows, err := tx.Query(ctx, `SELECT mr.server_id::text,mr.revision,mr.name,mr.transport,mr.command,mr.args,mr.url,mr.env_refs,mr.header_refs,mr.content_hash FROM relay_configuration_revision_mcp_servers pins JOIN mcp_revisions mr ON mr.id=pins.mcp_revision_id WHERE pins.relay_configuration_revision_id=$1 ORDER BY pins.position`, bundle.RelayConfigurationRevisionID)
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
	if err := attachRenderedRelayGovernance(&manifest, bundle, routingHash); err != nil {
		return bridgeprotocol.DesiredManifest{}, err
	}
	if err := manifest.Validate(true); err != nil {
		return bridgeprotocol.DesiredManifest{}, fmt.Errorf("resolve relay manifest: %w", err)
	}
	return manifest, nil
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
		if manifest.SchemaVersion == bridgeprotocol.ManifestSchemaVersionV2 {
			if manifest.RelayGovernance == nil {
				return ErrConflict
			}
			var pinned bridgeprotocol.RoutingBundle
			if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &pinned); err != nil {
				return ErrConflict
			}
			defaultProfileID := ""
			if pinned.DefaultProfileID != nil {
				defaultProfileID = *pinned.DefaultProfileID
			}
			published := make(map[string]string, len(pinned.Profiles))
			for _, profile := range pinned.Profiles {
				published[profile.ProfileID] = profile.ProfileRevisionID
			}
			candidate := RoutingBundleCandidate{
				Mode:                         pinned.Mode,
				RelayConfigurationRevisionID: pinned.RelayConfigurationRevisionID,
				GlobalPolicyRevisionID:       pinned.GlobalPolicyRevisionID,
				DefaultProfileID:             &defaultProfileID,
				ReplacePublishedProfiles:     len(pinned.Profiles) > 0,
				PublishedProfileRevisions:    published,
			}
			var bundle RoutingBundle
			var routingHash string
			if len(pinned.Profiles) == 0 {
				bundle, routingHash, err = s.RenderRelayConfigurationBundle(ctx, candidate)
			} else {
				bundle, routingHash, err = s.RenderCandidateRoutingBundle(ctx, candidate)
			}
			if err != nil {
				return err
			}
			if manifest.RelayGovernance == nil || manifest.RelayGovernance.RelayConfigurationRevisionID != bundle.RelayConfigurationRevisionID || manifest.RelayGovernance.RelayConfigurationHash != bundle.RelayConfigurationHash || manifest.RelayGovernance.RoutingHash != routingHash {
				return ErrConflict
			}
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
		if err := s.pool.QueryRow(ctx, `SELECT server_id::text,revision,name,transport,command,args,url,env_refs,header_refs,content_hash FROM mcp_revisions WHERE server_id=$1 AND revision=$2`, member.ServerID, member.Revision).Scan(&current.ServerID, &current.Revision, &current.Name, &current.Transport, &current.Command, &current.Args, &current.URL, &current.EnvRefs, &current.HeaderRefs, &current.ContentHash); errors.Is(err, pgx.ErrNoRows) {
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
	var currentProfileRevisionID string
	var profileClientKind string
	if err := tx.QueryRow(ctx, `SELECT p.revision,p.current_revision_id::text,p.client_kind FROM profiles p JOIN profile_revisions pr ON pr.id=p.current_revision_id WHERE p.id=$1 AND p.archived_at IS NULL AND NOT pr.pending_bindings FOR SHARE OF p`, profileID).Scan(&currentProfileRevision, &currentProfileRevisionID, &profileClientKind); errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	} else if err != nil {
		return domain.Operation{}, err
	}
	confirmed := make([]ConfirmedPreflight, 0, len(tokens))
	seenTargets := map[string]bool{}
	routingHash := ""
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
		if item.Manifest.Target.Runtime == domain.RuntimeClaude && profileClientKind != domain.RuntimeClaude {
			return domain.Operation{}, ErrConflict
		}
		if item.Manifest.Target.Runtime == domain.RuntimeCodex && profileClientKind != domain.RuntimeCodex {
			return domain.Operation{}, ErrConflict
		}
		if item.Manifest.RelayGovernance != nil {
			if routingHash != "" && routingHash != item.Manifest.RelayGovernance.RoutingHash {
				return domain.Operation{}, ErrConflict
			}
			routingHash = item.Manifest.RelayGovernance.RoutingHash
		}
		seenTargets[item.TargetID] = true
		confirmed = append(confirmed, item)
	}
	if len(confirmed) != 2 || !bridgeprotocol.IsSHA256(routingHash) {
		return domain.Operation{}, ErrConflict
	}
	targetIDs := make([]string, 0, len(confirmed))
	targetRuntime := make(map[string]string, len(confirmed))
	var skillTargetID string
	var relayTargetID string
	for _, item := range confirmed {
		targetIDs = append(targetIDs, item.TargetID)
		var nodeKind, runtime string
		if err := tx.QueryRow(ctx, `SELECT n.kind,t.runtime FROM targets t JOIN nodes n ON n.id=t.node_id WHERE t.id=$1 AND n.archived_at IS NULL`, item.TargetID).Scan(&nodeKind, &runtime); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.Operation{}, ErrConflict
			}
			return domain.Operation{}, err
		}
		targetRuntime[item.TargetID] = runtime
		if nodeKind != bridgeprotocol.NodeKindLocal || item.Manifest.Target.NodeKind != bridgeprotocol.NodeKindLocal || item.Manifest.Target.Runtime != runtime {
			return domain.Operation{}, ErrConflict
		}
		switch runtime {
		case profileClientKind:
			if skillTargetID != "" {
				return domain.Operation{}, ErrConflict
			}
			skillTargetID = item.TargetID
		case domain.RuntimeSharedRelay:
			if relayTargetID != "" || item.Manifest.RelayGovernance == nil {
				return domain.Operation{}, ErrConflict
			}
			relayTargetID = item.TargetID
		default:
			return domain.Operation{}, ErrConflict
		}
	}
	if skillTargetID == "" || relayTargetID == "" {
		return domain.Operation{}, ErrConflict
	}
	var expectedPublishedProfileRevisionID string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT profile_revision_id::text FROM published_profiles WHERE profile_id=$1),'')`, profileID).Scan(&expectedPublishedProfileRevisionID); err != nil {
		return domain.Operation{}, err
	}
	if err := lockActiveTargets(ctx, tx, targetIDs); err != nil {
		if errors.Is(err, ErrNotFound) {
			return domain.Operation{}, ErrConflict
		}
		return domain.Operation{}, err
	}
	operationID := uuid.NewString()
	metadata, _ := json.Marshal(map[string]any{"profileId": profileID, "profileRevisionId": currentProfileRevisionID, "targetCount": len(confirmed), "routingHash": routingHash, "expectedPublishedProfileRevisionId": expectedPublishedProfileRevisionID})
	if _, err := tx.Exec(ctx, `INSERT INTO operations(id,kind,status,source_id,idempotency_key,request_hash,metadata) VALUES($1,'apply','queued',$2,$3,$4,$5)`, operationID, profileID, nullableText(idempotencyKey), requestHash, jsonText(metadata)); err != nil {
		return domain.Operation{}, err
	}
	rowIDs := make(map[string]string, len(confirmed))
	for _, item := range confirmed {
		rowIDs[item.TargetID] = uuid.NewString()
	}
	for _, item := range confirmed {
		targetRequest, err := json.Marshal(map[string]any{"manifest": item.Manifest, "targetRevision": item.TargetRevision, "sourceKind": "profile_apply", "sourceId": profileID})
		if err != nil {
			return domain.Operation{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operation_targets(id,operation_id,target_id,request,governance_finalization_pending) VALUES($1,$2,$3,$4,true)`, rowIDs[item.TargetID], operationID, item.TargetID, jsonText(targetRequest)); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.Operation{}, ErrOperationActive
			}
			return domain.Operation{}, err
		}
	}
	for _, item := range confirmed {
		if targetRuntime[item.TargetID] != domain.RuntimeSharedRelay || skillTargetID == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE operation_targets SET depends_on_target_id=$2 WHERE id=$1`, rowIDs[item.TargetID], rowIDs[skillTargetID]); err != nil {
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

// CreateProfileSkillApplyOperation is the active Profile Apply path. It
// applies pinned Skills to the selected client targets only; shared MCP relay
// configuration is owned by Relay Configuration/mcpm and must never be part
// of this operation. CreateProfileApplyOperation remains available solely for
// historical governance fixtures and old migration compatibility.
func (s *Store) CreateProfileSkillApplyOperation(ctx context.Context, profileID string, tokens []string, idempotencyKey string) (domain.Operation, error) {
	if uuid.Validate(profileID) != nil || len(tokens) == 0 || len(tokens) > 100 {
		return domain.Operation{}, errors.New("Skill-only Profile Apply confirmation set is invalid")
	}
	tokenHashes := make([]string, 0, len(tokens))
	seenTokens := map[string]bool{}
	for _, token := range tokens {
		trimmed := strings.TrimSpace(token)
		hash := fmt.Sprintf("%x", security.TokenHash(trimmed))
		if trimmed == "" || seenTokens[hash] {
			return domain.Operation{}, ErrConflict
		}
		seenTokens[hash] = true
		tokenHashes = append(tokenHashes, hash)
	}
	sort.Strings(tokenHashes)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Operation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentRevision int64
	var currentRevisionID, clientKind string
	if err := tx.QueryRow(ctx, `SELECT p.revision,p.current_revision_id::text,p.client_kind FROM profiles p JOIN profile_revisions pr ON pr.id=p.current_revision_id WHERE p.id=$1 AND p.archived_at IS NULL AND NOT pr.pending_bindings FOR SHARE OF p`, profileID).Scan(&currentRevision, &currentRevisionID, &clientKind); errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, ErrNotFound
	} else if err != nil {
		return domain.Operation{}, err
	}
	if clientKind != domain.RuntimeClaude && clientKind != domain.RuntimeCodex {
		return domain.Operation{}, ErrConflict
	}

	confirmed := make([]ConfirmedPreflight, 0, len(tokens))
	seenTargets := map[string]bool{}
	nativeTargetSeen := false
	targetIDs := make([]string, 0, len(tokens))
	targetRequests := make(map[string]any, len(tokens))
	for _, token := range tokens {
		trimmed := strings.TrimSpace(token)
		var item ConfirmedPreflight
		var manifestJSON, diffJSON []byte
		if err := tx.QueryRow(ctx, `SELECT profile_id::text,profile_revision,target_id::text,target_revision,manifest_hash,manifest,diff FROM preflight_confirmations WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, security.TokenHash(trimmed)).Scan(&item.ProfileID, &item.ProfileRevision, &item.TargetID, &item.TargetRevision, &item.ManifestHash, &manifestJSON, &diffJSON); errors.Is(err, pgx.ErrNoRows) {
			return domain.Operation{}, ErrConflict
		} else if err != nil {
			return domain.Operation{}, err
		}
		if item.ProfileID != profileID || item.ProfileRevision != currentRevision || seenTargets[item.TargetID] {
			return domain.Operation{}, ErrConflict
		}
		item.Manifest, err = bridgeprotocol.DecodeManifest(manifestJSON, true)
		if err != nil || item.Manifest.Target.ID != item.TargetID || item.Manifest.ProfileID != profileID || item.Manifest.ProfileRevision != currentRevision {
			return domain.Operation{}, ErrConflict
		}
		_, canonicalHash, err := item.Manifest.Canonical()
		if err != nil || canonicalHash != item.ManifestHash {
			return domain.Operation{}, ErrConflict
		}
		if err := json.Unmarshal(diffJSON, &item.Diff); err != nil {
			return domain.Operation{}, err
		}
		if item.Manifest.RelayGovernance != nil || len(item.Manifest.MCPServers) != 0 || item.Manifest.Target.Runtime == domain.RuntimeSharedRelay {
			return domain.Operation{}, ErrConflict
		}
		var nodeKind, runtime string
		if err := tx.QueryRow(ctx, `SELECT n.kind,t.runtime FROM targets t JOIN nodes n ON n.id=t.node_id WHERE t.id=$1 AND n.archived_at IS NULL`, item.TargetID).Scan(&nodeKind, &runtime); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.Operation{}, ErrConflict
			}
			return domain.Operation{}, err
		}
		if runtime != item.Manifest.Target.Runtime || (runtime != domain.RuntimeClaude && runtime != domain.RuntimeCodex) || runtime != clientKind {
			return domain.Operation{}, ErrConflict
		}
		if nodeKind == bridgeprotocol.NodeKindLocal {
			if nativeTargetSeen {
				return domain.Operation{}, ErrConflict
			}
			nativeTargetSeen = true
		}
		seenTargets[item.TargetID] = true
		targetIDs = append(targetIDs, item.TargetID)
		targetRequests[item.TargetID] = map[string]any{"manifest": item.Manifest, "targetRevision": item.TargetRevision, "sourceKind": "profile_apply", "sourceId": profileID}
		confirmed = append(confirmed, item)
	}
	if len(confirmed) == 0 || !nativeTargetSeen {
		return domain.Operation{}, ErrConflict
	}

	request := map[string]any{"profileId": profileID, "profileRevision": currentRevision, "confirmationTokenHashes": tokenHashes}
	metadata := map[string]any{"profileId": profileID, "profileRevisionId": currentRevisionID, "targetCount": len(confirmed), "mode": "skills_only"}
	if err := lockActiveTargets(ctx, tx, targetIDs); err != nil {
		if errors.Is(err, ErrNotFound) {
			return domain.Operation{}, ErrConflict
		}
		return domain.Operation{}, err
	}
	result, err := s.createOperationTx(ctx, tx, CreateOperationInput{Kind: "apply", SourceID: profileID, IdempotencyKey: idempotencyKey, Request: request, Metadata: metadata, TargetIDs: targetIDs, TargetRequests: targetRequests}, true)
	if err != nil {
		return domain.Operation{}, err
	}
	if result.Replay {
		_ = tx.Rollback(ctx)
		return s.Operation(ctx, result.ID)
	}
	for _, token := range tokens {
		if _, err := tx.Exec(ctx, `UPDATE preflight_confirmations SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL`, security.TokenHash(strings.TrimSpace(token))); err != nil {
			return domain.Operation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Operation{}, err
	}
	return s.Operation(ctx, result.ID)
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
	if err := tx.QueryRow(ctx, `SELECT p.revision FROM profiles p JOIN profile_revisions pr ON pr.id=p.current_revision_id WHERE p.id=$1 AND p.archived_at IS NULL AND NOT pr.pending_bindings`, confirmed.ProfileID).Scan(&currentRevision); err != nil || currentRevision != confirmed.ProfileRevision {
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DesiredSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := pinDesiredSnapshotTx(ctx, tx, targetID, sourceKind, sourceID, operationTargetID, manifest, false)
	if err != nil {
		return domain.DesiredSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DesiredSnapshot{}, err
	}
	return s.DesiredSnapshot(ctx, id)
}

func pinDesiredSnapshotTx(ctx context.Context, tx pgx.Tx, targetID, sourceKind, sourceID, operationTargetID string, manifest bridgeprotocol.DesiredManifest, rejectExisting bool) (string, error) {
	body, hash, err := manifest.Canonical()
	if err != nil {
		return "", err
	}
	if manifest.Target.ID != targetID {
		return "", errors.New("manifest target does not match snapshot target")
	}
	var routingBundleCanonical any
	if manifest.SchemaVersion == bridgeprotocol.ManifestSchemaVersionV2 {
		if manifest.RelayGovernance == nil {
			return "", ErrConflict
		}
		var routingBundle bridgeprotocol.RoutingBundle
		if err := bridgeprotocol.DecodeGovernanceBody(manifest.RelayGovernance.RoutingBundle, &routingBundle); err != nil {
			return "", err
		}
		canonicalRouting, routingHash, err := routingBundle.Canonical()
		if err != nil || routingHash != manifest.RelayGovernance.RoutingHash {
			return "", ErrConflict
		}
		routingBundleCanonical = canonicalRouting
	}
	var operationTargetValue any
	if uuid.Validate(operationTargetID) == nil {
		operationTargetValue = operationTargetID
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id::text FROM desired_snapshots WHERE source_operation_target_id=$1`, operationTargetID).Scan(&existingID)
		if err == nil {
			if rejectExisting {
				return "", ErrConflict
			}
			return existingID, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	var lockedTargetID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM targets WHERE id=$1 FOR UPDATE`, targetID).Scan(&lockedTargetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0)+1 FROM desired_snapshots WHERE target_id=$1`, targetID).Scan(&revision); err != nil {
		return "", err
	}
	id := uuid.NewString()
	var sourceValue any
	if uuid.Validate(sourceID) == nil {
		sourceValue = sourceID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desired_snapshots(id,target_id,revision,source_kind,source_id,source_operation_target_id,profile_revision,manifest_schema_version,manifest_hash,manifest,routing_bundle_canonical) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, targetID, revision, sourceKind, sourceValue, operationTargetValue, manifest.ProfileRevision, manifest.SchemaVersion, hash, jsonText(body), routingBundleCanonical); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO target_desired_snapshots(target_id,snapshot_id,desired_revision,health,drift_summary,error_code,error_reason,updated_at) VALUES($1,$2,$3,'healthy','{}','','',now()) ON CONFLICT(target_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,desired_revision=EXCLUDED.desired_revision,health='healthy',drift_summary='{}',error_code='',error_reason='',updated_at=now()`, targetID, id, revision); err != nil {
		return "", err
	}
	return id, nil
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changed, err := updateTargetHealthTx(ctx, tx, targetID, health, errorCode, errorReason, drift, repaired)
	if err != nil {
		return false, err
	}
	return changed, tx.Commit(ctx)
}

type RelayProjectionPolicy string

const (
	RelayProjectionObserve RelayProjectionPolicy = "observe"
	RelayProjectionReset   RelayProjectionPolicy = "reset"
	RelayProjectionRetry   RelayProjectionPolicy = "retry"
)

func (s *Store) UpdateRelayProjection(ctx context.Context, targetID string, status bridgeprotocol.RelayStatus, health, errorCode, errorReason string, drift any, repaired bool, policy RelayProjectionPolicy) (bool, error) {
	if policy != RelayProjectionObserve && policy != RelayProjectionReset && policy != RelayProjectionRetry {
		return false, errors.New("relay projection policy is invalid")
	}
	if errorCode == "" {
		errorCode = status.ErrorCode
	}
	if errorReason == "" {
		errorReason = status.ErrorReason
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changed, err := updateTargetHealthTx(ctx, tx, targetID, health, errorCode, errorReason, drift, repaired)
	if err != nil {
		return false, err
	}
	var failureCount int
	var nextRetryAt *time.Time
	var suspended bool
	if err := tx.QueryRow(ctx, `SELECT relay_failure_count,relay_next_retry_at,relay_suspended FROM target_desired_snapshots WHERE target_id=$1 FOR UPDATE`, targetID).Scan(&failureCount, &nextRetryAt, &suspended); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if status.Healthy || health == bridgeprotocol.HealthHealthy {
		failureCount = 0
		nextRetryAt = nil
		suspended = false
	} else {
		switch policy {
		case RelayProjectionReset:
			failureCount = 0
			next := now.Add(5 * time.Minute)
			nextRetryAt = &next
			suspended = false
		case RelayProjectionRetry:
			failureCount++
			switch failureCount {
			case 1:
				next := now.Add(15 * time.Minute)
				nextRetryAt = &next
				suspended = false
			case 2:
				next := now.Add(time.Hour)
				nextRetryAt = &next
				suspended = false
			default:
				nextRetryAt = nil
				suspended = true
			}
		case RelayProjectionObserve:
			if nextRetryAt == nil && !suspended {
				next := now.Add(5 * time.Minute)
				nextRetryAt = &next
			}
		}
	}
	fullMemberCheck := status.Contract != ""
	memberJSON := []byte(`[]`)
	if fullMemberCheck {
		memberJSON, err = json.Marshal(status.MemberStatuses)
		if err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE target_desired_snapshots SET relay_failure_count=$2,relay_next_retry_at=$3,relay_suspended=$4,relay_last_member_check_at=CASE WHEN $5 THEN $6 ELSE relay_last_member_check_at END,relay_member_status=CASE WHEN $5 THEN $7::jsonb ELSE relay_member_status END,updated_at=now() WHERE target_id=$1`, targetID, failureCount, nextRetryAt, suspended, fullMemberCheck, now, jsonText(memberJSON)); err != nil {
		return false, err
	}
	return changed, tx.Commit(ctx)
}

func updateTargetHealthTx(ctx context.Context, tx pgx.Tx, targetID, health, errorCode, errorReason string, drift any, repaired bool) (bool, error) {
	errorReason = truncate(errorReason, 500)
	var driftJSON []byte
	if drift != nil {
		var err error
		driftJSON, err = json.Marshal(drift)
		if err != nil {
			return false, err
		}
	}
	var oldHealth, oldCode, oldReason string
	if err := tx.QueryRow(ctx, `SELECT health,error_code,error_reason FROM target_desired_snapshots WHERE target_id=$1 FOR UPDATE`, targetID).Scan(&oldHealth, &oldCode, &oldReason); err != nil {
		return false, err
	}
	changed := oldHealth != health || oldCode != errorCode || oldReason != errorReason
	if drift == nil {
		if _, err := tx.Exec(ctx, `UPDATE target_desired_snapshots SET health=$2,error_code=$3,error_reason=$4,last_reconciled_at=now(),last_repair_at=CASE WHEN $5 THEN now() ELSE last_repair_at END,updated_at=now() WHERE target_id=$1`, targetID, health, errorCode, truncate(errorReason, 500), repaired); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE target_desired_snapshots SET health=$2,error_code=$3,error_reason=$4,drift_summary=$5,last_reconciled_at=now(),last_repair_at=CASE WHEN $6 THEN now() ELSE last_repair_at END,updated_at=now() WHERE target_id=$1`, targetID, health, errorCode, truncate(errorReason, 500), jsonText(driftJSON), repaired); err != nil {
			return false, err
		}
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
	return changed, nil
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
