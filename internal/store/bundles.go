package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/profilebundle"
	"github.com/Junhao2314/toolhub/internal/security"
)

type BundleComponentDecision struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Hash     string `json:"hash"`
	Decision string `json:"decision"`
}

type BundlePreview struct {
	BundleHash        string                    `json:"bundleHash"`
	Kind              string                    `json:"kind"`
	ProfileName       string                    `json:"profileName"`
	CanonicalHash     string                    `json:"canonicalHash"`
	Origin            profilebundle.Origin      `json:"origin"`
	Components        []BundleComponentDecision `json:"components"`
	Duplicate         bool                      `json:"duplicate"`
	DuplicateReason   string                    `json:"duplicateReason,omitempty"`
	ExistingProfileID string                    `json:"existingProfileId,omitempty"`
	RequiresRename    bool                      `json:"requiresRename"`
	SuggestedName     string                    `json:"suggestedName,omitempty"`
	UpdateExisting    bool                      `json:"updateExisting"`
	PendingBindings   int                       `json:"pendingBindings"`
	ConfirmationToken string                    `json:"confirmationToken"`
	ExpiresAt         time.Time                 `json:"expiresAt"`
}

type BundleImportInput struct {
	ConfirmationToken string
	Name              string
	ConfirmDuplicate  bool
	ImportAsNew       bool
}

type RetentionPreview struct {
	CutoffAt             time.Time `json:"cutoffAt"`
	ProfileRevisions     int       `json:"profileRevisions"`
	MCPRevisions         int       `json:"mcpRevisions"`
	OrphanSecrets        int       `json:"orphanSecrets"`
	ProtectedProfileRows int       `json:"protectedProfileRows"`
}

func (s *Store) ExportProfileBundle(ctx context.Context, profileID, originLabel string, includeSecrets bool) ([]byte, error) {
	profile, err := s.Profile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if includeSecrets && profile.PendingBindings {
		return nil, ErrConflict
	}
	manifest := profilebundle.Manifest{
		Origin:  profilebundle.Origin{Label: strings.TrimSpace(originLabel), ExportedAt: time.Now().UTC()},
		Profile: profilebundle.Profile{Name: profile.Name, Description: profile.Description, SourceRevision: profile.Revision},
		Skills:  []profilebundle.Skill{}, MCPServers: []profilebundle.MCP{},
	}
	archives := map[string][]byte{}
	for _, pin := range profile.Skills {
		var archive []byte
		var name, description, sourceKind, sourceURL, sourceCommit string
		var rawProvenance []byte
		err := s.pool.QueryRow(ctx, `SELECT a.archive,sk.name,sk.description,ss.kind,ss.url,v.source_commit,v.provenance FROM skill_versions v JOIN skill_artifacts a ON a.id=v.artifact_id JOIN skills sk ON sk.id=v.skill_id JOIN skill_sources ss ON ss.id=sk.source_id WHERE v.id=$1`, pin.VersionID).Scan(&archive, &name, &description, &sourceKind, &sourceURL, &sourceCommit, &rawProvenance)
		if err != nil {
			return nil, err
		}
		manifest.Skills = append(manifest.Skills, profilebundle.Skill{Slug: pin.Slug, Name: name, Description: description, SHA256: pin.SHA256, ContentHash: pin.ContentHash, Provenance: safeBundleProvenance(sourceKind, sourceURL, sourceCommit, rawProvenance)})
		archives[pin.SHA256] = archive
	}
	var secretDoc *profilebundle.SecretDocument
	if includeSecrets {
		secretDoc = &profilebundle.SecretDocument{SchemaVersion: profilebundle.SchemaVersion, MCPServers: []profilebundle.SecretMCP{}}
		defer wipeBundleSecrets(secretDoc)
	}
	for _, pin := range profile.MCPServers {
		var item profilebundle.MCP
		var envRefs, headerRefs map[string]string
		var provenance []byte
		err := s.pool.QueryRow(ctx, `SELECT name,description,transport,command,args,url,env_slots,header_slots,env_refs,header_refs,provenance FROM mcp_revisions WHERE id=$1`, pin.RevisionID).Scan(&item.Name, &item.Description, &item.Transport, &item.Command, &item.Args, &item.URL, &item.EnvSlots, &item.HeaderSlots, &envRefs, &headerRefs, &provenance)
		if err != nil {
			return nil, err
		}
		item.Provenance = safeBundleProvenance("local", "", "", provenance)
		item.Key, err = profilebundle.MCPKey(item)
		if err != nil {
			return nil, err
		}
		manifest.MCPServers = append(manifest.MCPServers, item)
		if includeSecrets && (len(item.EnvSlots) > 0 || len(item.HeaderSlots) > 0) {
			secretItem := profilebundle.SecretMCP{Key: item.Key, Env: map[string]string{}, Headers: map[string]string{}}
			for key, id := range envRefs {
				value, err := s.secretValue(ctx, id)
				if err != nil {
					return nil, err
				}
				secretItem.Env[key] = string(value)
				clear(value)
			}
			for key, id := range headerRefs {
				value, err := s.secretValue(ctx, id)
				if err != nil {
					return nil, err
				}
				secretItem.Headers[key] = string(value)
				clear(value)
			}
			secretDoc.MCPServers = append(secretDoc.MCPServers, secretItem)
		}
	}
	body, err := profilebundle.Encode(manifest, archives, secretDoc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	kind := profilebundle.KindStandard
	if includeSecrets {
		kind = profilebundle.KindSecrets
	}
	metadata := map[string]any{"kind": kind, "bundleHash": hex.EncodeToString(sum[:]), "skills": len(manifest.Skills), "mcpServers": len(manifest.MCPServers), "includeSecrets": includeSecrets}
	if err := s.Audit(ctx, domain.AuditEvent{Action: "profile_bundle_export", ResourceType: "profile", ResourceID: profileID, Outcome: "success", Metadata: metadata}); err != nil {
		return nil, err
	}
	return body, nil
}

func wipeBundleSecrets(document *profilebundle.SecretDocument) {
	if document == nil {
		return
	}
	for index := range document.MCPServers {
		for key := range document.MCPServers[index].Env {
			document.MCPServers[index].Env[key] = ""
		}
		for key := range document.MCPServers[index].Headers {
			document.MCPServers[index].Headers[key] = ""
		}
	}
}

func (s *Store) PreviewProfileBundle(ctx context.Context, body []byte, ttl time.Duration) (BundlePreview, error) {
	if ttl <= 0 || ttl > 10*time.Minute {
		return BundlePreview{}, errors.New("Bundle confirmation TTL is invalid")
	}
	parsed, err := profilebundle.Parse(body)
	if err != nil {
		return BundlePreview{}, err
	}
	preview := BundlePreview{BundleHash: parsed.BundleHash, Kind: parsed.Manifest.Kind, ProfileName: parsed.Manifest.Profile.Name, CanonicalHash: parsed.Manifest.Profile.CanonicalHash, Origin: parsed.Manifest.Origin, Components: []BundleComponentDecision{}}
	for _, item := range parsed.Manifest.Skills {
		decision := "New"
		var existingSHA string
		err := s.pool.QueryRow(ctx, `SELECT a.canonical_sha256 FROM skills sk JOIN skill_versions v ON v.skill_id=sk.id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE sk.slug=$1 AND a.canonical_sha256=$2 LIMIT 1`, item.Slug, item.SHA256).Scan(&existingSHA)
		if err == nil {
			decision = "Reuse"
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return BundlePreview{}, err
		} else {
			var exists bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM skills WHERE slug=$1)`, item.Slug).Scan(&exists); err != nil {
				return BundlePreview{}, err
			}
			if exists {
				decision = "NewVersion"
			}
		}
		preview.Components = append(preview.Components, BundleComponentDecision{Kind: "skill", Name: item.Slug, Hash: item.SHA256, Decision: decision})
	}
	for _, item := range parsed.Manifest.MCPServers {
		reuse, err := s.bundleMCPRevisionExists(ctx, item)
		if err != nil {
			return BundlePreview{}, err
		}
		decision := "New"
		if reuse {
			decision = "Reuse"
		}
		preview.Components = append(preview.Components, BundleComponentDecision{Kind: "mcp", Name: item.Name, Hash: item.Key, Decision: decision})
		if parsed.Manifest.Kind == profilebundle.KindStandard {
			preview.PendingBindings += len(item.EnvSlots) + len(item.HeaderSlots)
		}
	}

	if err := s.pool.QueryRow(ctx, `SELECT profile_id::text FROM bundle_import_fingerprints WHERE bundle_hash=$1`, parsed.BundleHash).Scan(&preview.ExistingProfileID); err == nil {
		preview.Duplicate = true
		preview.DuplicateReason = "exact_bundle"
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return BundlePreview{}, err
	}
	if preview.ExistingProfileID == "" {
		var existingID string
		err := s.pool.QueryRow(ctx, `SELECT id::text FROM profiles WHERE name=$1`, parsed.Manifest.Profile.Name).Scan(&existingID)
		if err == nil {
			profile, err := s.Profile(ctx, existingID)
			if err != nil {
				return BundlePreview{}, err
			}
			portableHash, err := CanonicalProfileHash(profile.Name, profile.Description, profile.Skills, profile.MCPServers)
			if err != nil {
				return BundlePreview{}, err
			}
			preview.ExistingProfileID = existingID
			if portableHash == parsed.Manifest.Profile.CanonicalHash {
				if parsed.Manifest.Kind == profilebundle.KindStandard {
					preview.Duplicate = true
					preview.DuplicateReason = "same_profile_content"
				} else {
					preview.UpdateExisting = true
				}
			} else {
				preview.RequiresRename = true
				preview.SuggestedName = suggestedBundleProfileName(parsed.Manifest)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return BundlePreview{}, err
		}
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return BundlePreview{}, err
	}
	preview.ConfirmationToken = token
	preview.ExpiresAt = time.Now().UTC().Add(ttl)
	stored := preview
	stored.ConfirmationToken = ""
	encoded, err := json.Marshal(stored)
	if err != nil {
		return BundlePreview{}, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO bundle_import_confirmations(token_hash,bundle_hash,preview,expires_at) VALUES($1,$2,$3,$4)`, security.TokenHash(token), parsed.BundleHash, jsonText(encoded), preview.ExpiresAt); err != nil {
		return BundlePreview{}, err
	}
	return preview, nil
}

func (s *Store) ImportProfileBundle(ctx context.Context, body []byte, input BundleImportInput) (domain.Profile, error) {
	parsed, err := profilebundle.Parse(body)
	if err != nil {
		return domain.Profile{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Profile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var storedJSON []byte
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT bundle_hash,preview FROM bundle_import_confirmations WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, security.TokenHash(strings.TrimSpace(input.ConfirmationToken))).Scan(&storedHash, &storedJSON)
	if errors.Is(err, pgx.ErrNoRows) || storedHash != parsed.BundleHash {
		return domain.Profile{}, ErrConflict
	}
	if err != nil {
		return domain.Profile{}, err
	}
	var preview BundlePreview
	if err := json.Unmarshal(storedJSON, &preview); err != nil {
		return domain.Profile{}, err
	}
	if preview.Duplicate {
		if !input.ConfirmDuplicate || preview.ExistingProfileID == "" {
			return domain.Profile{}, ErrConflict
		}
		if err := consumeBundleToken(ctx, tx, input.ConfirmationToken); err != nil {
			return domain.Profile{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Profile{}, err
		}
		return s.Profile(ctx, preview.ExistingProfileID)
	}

	profileID := uuid.NewString()
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = parsed.Manifest.Profile.Name
	}
	expectedRevision := int64(0)
	if preview.UpdateExisting && !input.ImportAsNew {
		profileID = preview.ExistingProfileID
		var currentName string
		if err := tx.QueryRow(ctx, `SELECT name,revision FROM profiles WHERE id=$1 FOR UPDATE`, profileID).Scan(&currentName, &expectedRevision); err != nil || currentName != parsed.Manifest.Profile.Name {
			return domain.Profile{}, ErrConflict
		}
		name = currentName
	} else if preview.RequiresRename || (preview.UpdateExisting && input.ImportAsNew) {
		if name == "" || name == parsed.Manifest.Profile.Name {
			return domain.Profile{}, ErrConflict
		}
	}

	profileInput := ProfileInput{Name: name, Description: parsed.Manifest.Profile.Description, SkillVersionIDs: map[string]string{}, MCPRevisionIDs: map[string]string{}, Revision: expectedRevision, PendingBindings: preview.PendingBindings > 0}
	for _, item := range parsed.Manifest.Skills {
		pkg := parsed.Packages[item.SHA256]
		source := SourceInput{Kind: "local", Name: "bundle:" + parsed.Manifest.Origin.Label, Metadata: map[string]any{"bundleHash": parsed.BundleHash}}
		provenance := map[string]any{"source": item.Provenance.Source, "provider": item.Provenance.Provider, "url": item.Provenance.URL, "commit": item.Provenance.Commit, "bundleHash": parsed.BundleHash}
		skillID, versionID, _, _, err := s.importSkillTx(ctx, tx, source, pkg, provenance, false)
		if err != nil {
			return domain.Profile{}, err
		}
		profileInput.SkillIDs = append(profileInput.SkillIDs, skillID)
		profileInput.SkillVersionIDs[skillID] = versionID
	}
	mcpPins := map[string]string{}
	for _, item := range parsed.Manifest.MCPServers {
		serverID, revisionID, err := s.importBundleMCPTx(ctx, tx, item, parsed.Secrets, parsed.Manifest.Kind == profilebundle.KindSecrets && preview.UpdateExisting && !input.ImportAsNew)
		if err != nil {
			return domain.Profile{}, err
		}
		profileInput.MCPServerIDs = append(profileInput.MCPServerIDs, serverID)
		profileInput.MCPRevisionIDs[serverID] = revisionID
		mcpPins[item.Key] = revisionID
	}
	profileID, profileRevisionID, err := s.saveProfileTx(ctx, tx, profileID, profileInput)
	if err != nil {
		return domain.Profile{}, err
	}
	if preview.PendingBindings > 0 {
		for _, item := range parsed.Manifest.MCPServers {
			for namespace, keys := range map[string][]string{"env": item.EnvSlots, "header": item.HeaderSlots} {
				for _, key := range keys {
					slotHash := bundleSlotHash(item.Key, namespace, key)
					if _, err := tx.Exec(ctx, `INSERT INTO pending_secret_bindings(profile_revision_id,mcp_revision_id,namespace,key,slot_hash) VALUES($1,$2,$3,$4,$5)`, profileRevisionID, mcpPins[item.Key], namespace, key, slotHash); err != nil {
						return domain.Profile{}, err
					}
				}
			}
		}
	}
	metadata, _ := json.Marshal(map[string]any{"kind": parsed.Manifest.Kind, "skills": len(parsed.Manifest.Skills), "mcpServers": len(parsed.Manifest.MCPServers), "pendingBindings": preview.PendingBindings})
	if _, err := tx.Exec(ctx, `INSERT INTO bundle_import_fingerprints(bundle_hash,profile_id,profile_revision_id,canonical_hash,metadata) VALUES($1,$2,$3,$4,$5)`, parsed.BundleHash, profileID, profileRevisionID, parsed.Manifest.Profile.CanonicalHash, jsonText(metadata)); err != nil {
		return domain.Profile{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'profile_bundle_import','profile',$2,'success',$3)`, uuid.NewString(), profileID, jsonText(metadata)); err != nil {
		return domain.Profile{}, err
	}
	if err := consumeBundleToken(ctx, tx, input.ConfirmationToken); err != nil {
		return domain.Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Profile{}, err
	}
	return s.Profile(ctx, profileID)
}

func (s *Store) importBundleMCPTx(ctx context.Context, tx pgx.Tx, item profilebundle.MCP, secrets profilebundle.SecretDocument, advanceCurrent bool) (string, string, error) {
	var serverID string
	var currentRevision int64
	err := tx.QueryRow(ctx, `SELECT id::text,revision FROM mcp_servers WHERE name=$1 FOR UPDATE`, item.Name).Scan(&serverID, &currentRevision)
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		created = true
		serverID = uuid.NewString()
		currentRevision = 0
	} else if err != nil {
		return "", "", err
	}
	if !created {
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0) FROM mcp_revisions WHERE server_id=$1`, serverID).Scan(&currentRevision); err != nil {
			return "", "", err
		}
	}
	secretItem := profilebundle.SecretMCP{Env: map[string]string{}, Headers: map[string]string{}}
	for _, candidate := range secrets.MCPServers {
		if candidate.Key == item.Key {
			secretItem = candidate
			break
		}
	}
	envRefs, err := s.createBundleSecretsTx(ctx, tx, serverID, "env", "mcp-env", secretItem.Env)
	if err != nil {
		return "", "", err
	}
	headerRefs, err := s.createBundleSecretsTx(ctx, tx, serverID, "header", "mcp-header", secretItem.Headers)
	if err != nil {
		return "", "", err
	}
	if !created && len(envRefs) == 0 && len(headerRefs) == 0 {
		if revisionID, ok, err := bundleMCPRevisionMatchTx(ctx, tx, serverID, item); err != nil {
			return "", "", err
		} else if ok {
			return serverID, revisionID, nil
		}
	}
	input := MCPInput{Name: item.Name, Description: item.Description, Transport: item.Transport, Command: item.Command, Args: item.Args, URL: item.URL}
	contentHash := MCPContentHash(input, envRefs, headerRefs)
	argsJSON, _ := json.Marshal(item.Args)
	envJSON, _ := json.Marshal(envRefs)
	headerJSON, _ := json.Marshal(headerRefs)
	provenance, _ := json.Marshal(map[string]any{"source": "bundle", "memberSource": item.Provenance.Source, "provider": item.Provenance.Provider, "url": item.Provenance.URL, "commit": item.Provenance.Commit})
	revisionID := uuid.NewString()
	revision := currentRevision + 1
	if created {
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_servers(id,current_revision_id,name,description,revision,transport,command,args,url,env_refs,header_refs,content_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, serverID, revisionID, item.Name, item.Description, revision, item.Transport, item.Command, jsonText(argsJSON), item.URL, jsonText(envJSON), jsonText(headerJSON), contentHash); err != nil {
			return "", "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_revisions(id,server_id,revision,name,description,transport,command,args,url,env_slots,header_slots,env_refs,header_refs,content_hash,provenance) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, revisionID, serverID, revision, item.Name, item.Description, item.Transport, item.Command, jsonText(argsJSON), item.URL, item.EnvSlots, item.HeaderSlots, jsonText(envJSON), jsonText(headerJSON), contentHash, jsonText(provenance)); err != nil {
		return "", "", err
	}
	if !created && advanceCurrent {
		if _, err := tx.Exec(ctx, `UPDATE mcp_servers SET current_revision_id=$2,revision=$3,description=$4,transport=$5,command=$6,args=$7,url=$8,env_refs=$9,header_refs=$10,content_hash=$11,updated_at=now() WHERE id=$1`, serverID, revisionID, revision, item.Description, item.Transport, item.Command, jsonText(argsJSON), item.URL, jsonText(envJSON), jsonText(headerJSON), contentHash); err != nil {
			return "", "", err
		}
	}
	return serverID, revisionID, nil
}

func (s *Store) createBundleSecretsTx(ctx context.Context, tx pgx.Tx, serverID, namespace, kind string, values map[string]string) (map[string]string, error) {
	refs := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		secretID := uuid.NewString()
		ciphertext, err := s.cipher.Encrypt([]byte(values[key]), secretID)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("mcp:%s:%s:%s:%s", serverID, namespace, key, secretID)
		if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata) VALUES($1,$2,$3,$4,'{"writeOnly":true,"source":"profile-bundle"}')`, secretID, name, kind, ciphertext); err != nil {
			return nil, err
		}
		refs[key] = secretID
	}
	return refs, nil
}

func (s *Store) bundleMCPRevisionExists(ctx context.Context, item profilebundle.MCP) (bool, error) {
	var serverID string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM mcp_servers WHERE name=$1`, item.Name).Scan(&serverID); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	_, ok, err := bundleMCPRevisionMatch(ctx, s.pool, serverID, item)
	return ok, err
}

func bundleMCPRevisionMatchTx(ctx context.Context, tx pgx.Tx, serverID string, item profilebundle.MCP) (string, bool, error) {
	return bundleMCPRevisionMatch(ctx, tx, serverID, item)
}

func bundleMCPRevisionMatch(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, serverID string, item profilebundle.MCP) (string, bool, error) {
	rows, err := query.Query(ctx, `SELECT id::text,name,description,transport,command,args,url,env_slots,header_slots FROM mcp_revisions WHERE server_id=$1 ORDER BY revision DESC`, serverID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var candidate profilebundle.MCP
		if err := rows.Scan(&id, &candidate.Name, &candidate.Description, &candidate.Transport, &candidate.Command, &candidate.Args, &candidate.URL, &candidate.EnvSlots, &candidate.HeaderSlots); err != nil {
			return "", false, err
		}
		key, err := profilebundle.MCPKey(candidate)
		if err != nil {
			return "", false, err
		}
		if key == item.Key {
			return id, true, nil
		}
	}
	return "", false, rows.Err()
}

func safeBundleProvenance(sourceKind, sourceURL, commit string, raw []byte) profilebundle.Provenance {
	value := profilebundle.Provenance{Source: strings.ToLower(strings.TrimSpace(sourceKind)), URL: strings.TrimSpace(sourceURL), Commit: strings.TrimSpace(commit)}
	var metadata map[string]any
	_ = json.Unmarshal(raw, &metadata)
	if source, ok := metadata["source"].(string); ok {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "claude" || source == "codex" || source == "hermes" || source == "git" || source == "skillsmp" || source == "xiaping" || source == "skillhub" || source == "zip" || source == "local" || source == "bundle" {
			value.Source = source
		}
	}
	if provider, ok := metadata["provider"].(string); ok && len(provider) <= 120 {
		value.Provider = strings.TrimSpace(provider)
	}
	if value.Source == "" {
		value.Source = "local"
	}
	if parsed, err := url.Parse(value.URL); value.URL != "" && (err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil) {
		value.URL = ""
	}
	if len(value.Commit) > 200 {
		value.Commit = ""
	}
	return value
}

func suggestedBundleProfileName(manifest profilebundle.Manifest) string {
	suffix := strings.TrimSpace(manifest.Origin.Label) + " " + manifest.Origin.ExportedAt.UTC().Format("20060102")
	name := strings.TrimSpace(manifest.Profile.Name + " - " + suffix)
	if len(name) > 120 {
		name = name[:120]
	}
	return strings.TrimSpace(name)
}

func consumeBundleToken(ctx context.Context, tx pgx.Tx, token string) error {
	command, err := tx.Exec(ctx, `UPDATE bundle_import_confirmations SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL`, security.TokenHash(strings.TrimSpace(token)))
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func bundleSlotHash(mcpKey, namespace, key string) string {
	sum := sha256.Sum256([]byte(mcpKey + "\x00" + namespace + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CompletePendingSecretBindings(ctx context.Context, profileID string, expectedRevision int64, values map[string]string) (domain.Profile, error) {
	profile, err := s.Profile(ctx, profileID)
	if err != nil {
		return domain.Profile{}, err
	}
	if profile.Revision != expectedRevision || !profile.PendingBindings {
		return domain.Profile{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Profile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT mcp_revision_id::text,namespace,key,slot_hash FROM pending_secret_bindings WHERE profile_revision_id=$1 ORDER BY mcp_revision_id,namespace,key`, profile.CurrentRevisionID)
	if err != nil {
		return domain.Profile{}, err
	}
	type pendingSlot struct{ revisionID, namespace, key, hash string }
	var pending []pendingSlot
	for rows.Next() {
		var item pendingSlot
		if err := rows.Scan(&item.revisionID, &item.namespace, &item.key, &item.hash); err != nil {
			rows.Close()
			return domain.Profile{}, err
		}
		if _, ok := values[item.hash]; !ok {
			rows.Close()
			return domain.Profile{}, ErrConflict
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.Profile{}, err
	}
	rows.Close()
	if len(values) != len(pending) {
		return domain.Profile{}, ErrConflict
	}
	byRevision := map[string]map[string]map[string]string{}
	for _, item := range pending {
		if byRevision[item.revisionID] == nil {
			byRevision[item.revisionID] = map[string]map[string]string{"env": {}, "header": {}}
		}
		byRevision[item.revisionID][item.namespace][item.key] = values[item.hash]
	}
	input := ProfileInput{Name: profile.Name, Description: profile.Description, Revision: profile.Revision, SkillVersionIDs: map[string]string{}, MCPRevisionIDs: map[string]string{}, PendingBindings: false}
	for _, pin := range profile.Skills {
		input.SkillIDs = append(input.SkillIDs, pin.SkillID)
		input.SkillVersionIDs[pin.SkillID] = pin.VersionID
	}
	for _, pin := range profile.MCPServers {
		input.MCPServerIDs = append(input.MCPServerIDs, pin.ServerID)
		input.MCPRevisionIDs[pin.ServerID] = pin.RevisionID
		if slots := byRevision[pin.RevisionID]; len(slots) > 0 {
			item := profilebundle.MCP{Name: pin.Name, Description: pin.Description, Transport: pin.Transport, Command: pin.Command, Args: pin.Args, URL: pin.URL, EnvSlots: pin.EnvKeys, HeaderSlots: pin.HeaderKeys}
			item.Key, err = profilebundle.MCPKey(item)
			if err != nil {
				return domain.Profile{}, err
			}
			secretDoc := profilebundle.SecretDocument{SchemaVersion: profilebundle.SchemaVersion, MCPServers: []profilebundle.SecretMCP{{Key: item.Key, Env: slots["env"], Headers: slots["header"]}}}
			_, revisionID, err := s.importBundleMCPTx(ctx, tx, item, secretDoc, true)
			if err != nil {
				return domain.Profile{}, err
			}
			input.MCPRevisionIDs[pin.ServerID] = revisionID
		}
	}
	if _, _, err := s.saveProfileTx(ctx, tx, profileID, input); err != nil {
		return domain.Profile{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"profileRevision": profile.Revision, "bindingCount": len(pending)})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'profile_secret_bindings_completed','profile',$2,'success',$3)`, uuid.NewString(), profileID, jsonText(metadata)); err != nil {
		return domain.Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Profile{}, err
	}
	for key := range values {
		values[key] = ""
	}
	return s.Profile(ctx, profileID)
}

func (s *Store) PendingSecretBindings(ctx context.Context, profileID string) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT psb.profile_revision_id::text AS "profileRevisionId",psb.mcp_revision_id::text AS "mcpRevisionId",psb.namespace,psb.key,psb.slot_hash AS "slotHash",(psb.secret_id IS NOT NULL) AS bound,psb.bound_at AS "boundAt" FROM pending_secret_bindings psb JOIN profiles p ON p.current_revision_id=psb.profile_revision_id WHERE p.id=$1 ORDER BY psb.mcp_revision_id,psb.namespace,psb.key`, profileID)
}

func (s *Store) PreviewRetention(ctx context.Context, now time.Time) (RetentionPreview, error) {
	cutoff := now.UTC().Add(-60 * 24 * time.Hour)
	preview := RetentionPreview{CutoffAt: cutoff}
	queries := []struct {
		destination *int
		query       string
		args        []any
	}{
		{&preview.ProfileRevisions, `SELECT count(*) FROM profile_revisions pr WHERE pr.created_at<$1 AND pr.id NOT IN (SELECT current_revision_id FROM profiles) AND pr.id NOT IN (SELECT profile_revision_id FROM bundle_import_fingerprints WHERE retain_until>$1) AND (SELECT count(*) FROM profile_revisions keep WHERE keep.profile_id=pr.profile_id AND keep.created_at>=pr.created_at AND keep.id<>pr.id) >= 20`, []any{cutoff}},
		{&preview.MCPRevisions, `SELECT count(*) FROM mcp_revisions mr WHERE mr.created_at<$1 AND mr.id NOT IN (SELECT current_revision_id FROM mcp_servers) AND NOT EXISTS (SELECT 1 FROM profile_revision_mcp_servers p WHERE p.mcp_revision_id=mr.id)`, []any{cutoff}},
		{&preview.OrphanSecrets, `SELECT count(*) FROM encrypted_secrets es WHERE es.created_at<$1 AND es.kind IN ('mcp-env','mcp-header') AND NOT EXISTS (SELECT 1 FROM mcp_revisions mr WHERE mr.env_refs ? es.id::text OR mr.header_refs ? es.id::text) AND NOT EXISTS (SELECT 1 FROM pending_secret_bindings psb WHERE psb.secret_id=es.id)`, []any{now.UTC().Add(-30 * 24 * time.Hour)}},
	}
	for _, item := range queries {
		if err := s.pool.QueryRow(ctx, item.query, item.args...).Scan(item.destination); err != nil {
			return RetentionPreview{}, err
		}
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM profile_revisions pr WHERE pr.created_at<$1 AND (pr.id IN (SELECT current_revision_id FROM profiles) OR pr.id IN (SELECT profile_revision_id FROM bundle_import_fingerprints WHERE retain_until>$1))`, cutoff).Scan(&preview.ProtectedProfileRows); err != nil {
		return RetentionPreview{}, err
	}
	return preview, nil
}

func (s *Store) RunRetention(ctx context.Context, now time.Time) (RetentionPreview, error) {
	preview, err := s.PreviewRetention(ctx, now)
	if err != nil {
		return RetentionPreview{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RetentionPreview{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cutoff := preview.CutoffAt
	if _, err := tx.Exec(ctx, `DELETE FROM profile_revisions pr WHERE pr.created_at<$1 AND pr.id NOT IN (SELECT current_revision_id FROM profiles) AND pr.id NOT IN (SELECT profile_revision_id FROM bundle_import_fingerprints WHERE retain_until>$1) AND (SELECT count(*) FROM profile_revisions keep WHERE keep.profile_id=pr.profile_id AND keep.created_at>=pr.created_at AND keep.id<>pr.id) >= 20`, cutoff); err != nil {
		return RetentionPreview{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mcp_revisions mr WHERE mr.created_at<$1 AND mr.id NOT IN (SELECT current_revision_id FROM mcp_servers) AND NOT EXISTS (SELECT 1 FROM profile_revision_mcp_servers p WHERE p.mcp_revision_id=mr.id)`, cutoff); err != nil {
		return RetentionPreview{}, err
	}
	secretCutoff := now.UTC().Add(-30 * 24 * time.Hour)
	if _, err := tx.Exec(ctx, `DELETE FROM encrypted_secrets es WHERE es.created_at<$1 AND es.kind IN ('mcp-env','mcp-header') AND NOT EXISTS (SELECT 1 FROM mcp_revisions mr WHERE mr.env_refs ? es.id::text OR mr.header_refs ? es.id::text) AND NOT EXISTS (SELECT 1 FROM pending_secret_bindings psb WHERE psb.secret_id=es.id)`, secretCutoff); err != nil {
		return RetentionPreview{}, err
	}
	counts, _ := json.Marshal(map[string]any{"profileRevisions": preview.ProfileRevisions, "mcpRevisions": preview.MCPRevisions, "orphanSecrets": preview.OrphanSecrets})
	if _, err := tx.Exec(ctx, `INSERT INTO retention_runs(id,kind,status,cutoff_at,counts) VALUES($1,'history_gc','succeeded',$2,$3)`, uuid.NewString(), cutoff, jsonText(counts)); err != nil {
		return RetentionPreview{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RetentionPreview{}, err
	}
	return preview, nil
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
