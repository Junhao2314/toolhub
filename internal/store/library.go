package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/skills"
)

type SourceInput struct {
	Kind         string         `json:"kind"`
	Name         string         `json:"name"`
	URL          string         `json:"url,omitempty"`
	Subdirectory string         `json:"subdirectory,omitempty"`
	Commit       string         `json:"commit,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func (s *Store) RefreshableSkillSources(ctx context.Context) ([]SourceInput, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind,name,url,subdirectory,current_commit,metadata FROM skill_sources WHERE kind IN ('git','skillsmp','xiaping','skillhub') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SourceInput
	for rows.Next() {
		var source SourceInput
		var metadata []byte
		if err := rows.Scan(&source.Kind, &source.Name, &source.URL, &source.Subdirectory, &source.Commit, &metadata); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &source.Metadata); err != nil {
				return nil, err
			}
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

func (s *Store) ImportSkill(ctx context.Context, source SourceInput, pkg skills.Package, provenance map[string]any) (domain.Skill, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Skill{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	skillID, _, created, _, err := s.importSkillTx(ctx, tx, source, pkg, provenance, true)
	if err != nil {
		return domain.Skill{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Skill{}, false, err
	}
	skill, err := s.Skill(ctx, skillID)
	return skill, created, err
}

func (s *Store) importSkillTx(ctx context.Context, tx pgx.Tx, source SourceInput, pkg skills.Package, provenance map[string]any, advanceCurrent bool) (string, string, bool, bool, error) {
	if err := validateSourceInput(source); err != nil {
		return "", "", false, false, err
	}
	if len(pkg.CanonicalZIP) == 0 || len(pkg.CanonicalZIP) > int(skills.DefaultLimits.MaxArchiveBytes) {
		return "", "", false, false, errors.New("canonical Skill archive is invalid")
	}
	manifest, err := json.Marshal(pkg.Manifest)
	if err != nil {
		return "", "", false, false, err
	}
	report, err := json.Marshal(pkg.Report)
	if err != nil {
		return "", "", false, false, err
	}
	if provenance == nil {
		provenance = map[string]any{}
	}
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return "", "", false, false, err
	}
	metadata := []byte("{}")
	if source.Metadata != nil {
		if metadata, err = json.Marshal(source.Metadata); err != nil {
			return "", "", false, false, err
		}
	}

	var skillID, sourceID, currentVersionID string
	err = tx.QueryRow(ctx, `SELECT sk.id::text,sk.source_id::text,sk.current_version_id::text FROM skills sk JOIN skill_sources ss ON ss.id=sk.source_id WHERE sk.slug=$1 AND sk.archived_at IS NULL FOR UPDATE`, pkg.Slug).Scan(&skillID, &sourceID, &currentVersionID)
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		created = true
		sourceID, skillID = uuid.NewString(), uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO skill_sources(id,kind,name,url,subdirectory,current_commit,metadata) VALUES($1,$2,$3,$4,$5,$6,$7)`, sourceID, source.Kind, source.Name, source.URL, source.Subdirectory, source.Commit, jsonText(metadata)); err != nil {
			return "", "", false, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO skills(id,source_id,slug,name,description,tags) VALUES($1,$2,$3,$4,$5,'{}'::text[])`, skillID, sourceID, pkg.Slug, pkg.Name, pkg.Description); err != nil {
			return "", "", false, false, err
		}
	} else if err != nil {
		return "", "", false, false, err
	} else {
		var currentSHA string
		if err := tx.QueryRow(ctx, `SELECT a.canonical_sha256 FROM skill_versions v JOIN skill_artifacts a ON a.id=v.artifact_id WHERE v.id=$1`, currentVersionID).Scan(&currentSHA); err != nil {
			return "", "", false, false, err
		}
		if currentSHA == pkg.SHA256 {
			if advanceCurrent && source.Commit != "" {
				if _, err := tx.Exec(ctx, `UPDATE skill_sources SET current_commit=$2,updated_at=now() WHERE id=$1`, sourceID, source.Commit); err != nil {
					return "", "", false, false, err
				}
			}
			return skillID, currentVersionID, false, true, nil
		}
	}

	artifactID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO skill_artifacts(id,canonical_sha256,content_hash,archive,size_bytes,manifest,scan_report) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(canonical_sha256) DO NOTHING`, artifactID, pkg.SHA256, pkg.ContentHash, pkg.CanonicalZIP, len(pkg.CanonicalZIP), jsonText(manifest), jsonText(report)); err != nil {
		return "", "", false, false, err
	}
	var storedContentHash string
	var storedSize int64
	if err := tx.QueryRow(ctx, `SELECT id::text,content_hash,size_bytes FROM skill_artifacts WHERE canonical_sha256=$1`, pkg.SHA256).Scan(&artifactID, &storedContentHash, &storedSize); err != nil {
		return "", "", false, false, err
	}
	if storedContentHash != pkg.ContentHash || storedSize != int64(len(pkg.CanonicalZIP)) {
		return "", "", false, false, errors.New("existing immutable Skill artifact does not match canonical package")
	}
	if !advanceCurrent {
		var existingVersion string
		err := tx.QueryRow(ctx, `SELECT id::text FROM skill_versions WHERE skill_id=$1 AND artifact_id=$2 ORDER BY created_at,id LIMIT 1`, skillID, artifactID).Scan(&existingVersion)
		if err == nil {
			return skillID, existingVersion, created, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, false, err
		}
	}
	versionID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO skill_versions(id,skill_id,artifact_id,source_commit,provenance) VALUES($1,$2,$3,$4,$5) ON CONFLICT(skill_id,artifact_id,source_commit) DO NOTHING`, versionID, skillID, artifactID, source.Commit, jsonText(provenanceJSON)); err != nil {
		return "", "", false, false, err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM skill_versions WHERE skill_id=$1 AND artifact_id=$2 AND source_commit=$3`, skillID, artifactID, source.Commit).Scan(&versionID); err != nil {
		return "", "", false, false, err
	}
	if created || advanceCurrent {
		if _, err := tx.Exec(ctx, `UPDATE skills SET name=$2,description=$3,current_version_id=$4,updated_at=now() WHERE id=$1`, skillID, pkg.Name, pkg.Description, versionID); err != nil {
			return "", "", false, false, err
		}
	}
	if advanceCurrent {
		if _, err := tx.Exec(ctx, `UPDATE skill_sources SET kind=$2,name=$3,url=$4,subdirectory=$5,current_commit=$6,metadata=$7,updated_at=now() WHERE id=$1`, sourceID, source.Kind, source.Name, source.URL, source.Subdirectory, source.Commit, jsonText(metadata)); err != nil {
			return "", "", false, false, err
		}
	}
	return skillID, versionID, created, false, nil
}

func validateSourceInput(input SourceInput) error {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	switch input.Kind {
	case "zip", "local":
		if input.URL != "" {
			return errors.New("ZIP and local Skill sources cannot contain a URL")
		}
	case "git", "skillsmp", "xiaping", "skillhub":
		parsed, err := url.Parse(strings.TrimSpace(input.URL))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
			return errors.New("remote Skill source requires an HTTPS URL without credentials")
		}
	default:
		return errors.New("unsupported Skill source kind")
	}
	if strings.TrimSpace(input.Name) == "" {
		return errors.New("Skill source name is required")
	}
	return nil
}

func (s *Store) ListSkills(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT sk.id::text,sk.slug,sk.name,sk.description,sk.tags,ss.kind AS "sourceKind",ss.url AS "sourceUrl",ss.current_commit AS "sourceCommit",v.id::text AS "currentVersionId",a.canonical_sha256 AS "currentSha256",a.content_hash AS "currentContentHash",a.manifest,a.scan_report AS "scanReport",sk.created_at AS "createdAt",sk.updated_at AS "updatedAt" FROM skills sk JOIN skill_sources ss ON ss.id=sk.source_id JOIN skill_versions v ON v.id=sk.current_version_id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE sk.archived_at IS NULL ORDER BY lower(sk.name),sk.id`)
}

func (s *Store) Skill(ctx context.Context, id string) (domain.Skill, error) {
	var skill domain.Skill
	err := s.pool.QueryRow(ctx, `SELECT sk.id::text,sk.slug,sk.name,sk.description,sk.tags,ss.kind,ss.url,ss.current_commit,v.id::text,a.canonical_sha256,a.content_hash,a.manifest,a.scan_report,sk.created_at,sk.updated_at FROM skills sk JOIN skill_sources ss ON ss.id=sk.source_id JOIN skill_versions v ON v.id=sk.current_version_id JOIN skill_artifacts a ON a.id=v.artifact_id WHERE sk.id=$1 AND sk.archived_at IS NULL`, id).Scan(
		&skill.ID, &skill.Slug, &skill.Name, &skill.Description, &skill.Tags, &skill.SourceKind, &skill.SourceURL, &skill.SourceCommit, &skill.CurrentVersionID, &skill.CurrentSHA256, &skill.CurrentContentHash, &skill.Manifest, &skill.ScanReport, &skill.CreatedAt, &skill.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Skill{}, ErrNotFound
	}
	return skill, err
}

func normalizeSkillTags(tags []string) ([]string, error) {
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" || len(tag) > 64 || !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`).MatchString(tag) {
			return nil, errors.New("Skill tags must be lowercase slugs up to 64 characters")
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	if len(result) > 50 {
		return nil, errors.New("Skill tags exceed the safety limit")
	}
	sort.Strings(result)
	return result, nil
}

func (s *Store) UpdateSkillTags(ctx context.Context, id string, tags []string) (domain.Skill, error) {
	normalized, err := normalizeSkillTags(tags)
	if err != nil {
		return domain.Skill{}, err
	}
	command, err := s.pool.Exec(ctx, `UPDATE skills SET tags=$2,updated_at=now() WHERE id=$1 AND archived_at IS NULL`, id, normalized)
	if err != nil {
		return domain.Skill{}, err
	}
	if command.RowsAffected() != 1 {
		return domain.Skill{}, ErrNotFound
	}
	return s.Skill(ctx, id)
}

func (s *Store) SkillArtifact(ctx context.Context, versionID string) ([]byte, string, error) {
	var archive []byte
	var sha string
	err := s.pool.QueryRow(ctx, `SELECT a.archive,a.canonical_sha256 FROM skill_versions v JOIN skill_artifacts a ON a.id=v.artifact_id WHERE v.id=$1`, versionID).Scan(&archive, &sha)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return archive, sha, err
}

type MCPInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	URL         string            `json:"url,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Provenance  map[string]any    `json:"-"`
}

var mcpNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func NormalizeMCPInput(input MCPInput) (MCPInput, error) {
	input.Name = strings.ToLower(strings.TrimSpace(input.Name))
	input.Description = strings.TrimSpace(input.Description)
	if !mcpNamePattern.MatchString(input.Name) || strings.HasPrefix(input.Name, ".") || strings.HasPrefix(input.Name, "toolhub-") {
		return MCPInput{}, errors.New("MCP server name is invalid or protected")
	}
	switch input.Transport {
	case "stdio":
		if strings.TrimSpace(input.Command) == "" || input.URL != "" {
			return MCPInput{}, errors.New("stdio MCP requires command and forbids URL")
		}
	case "http", "sse":
		if strings.TrimSpace(input.URL) == "" || input.Command != "" || len(input.Args) > 0 {
			return MCPInput{}, errors.New("network MCP requires URL and forbids command/args")
		}
	default:
		return MCPInput{}, errors.New("unsupported MCP transport")
	}
	if input.Args == nil {
		input.Args = []string{}
	}
	return input, nil
}

func (s *Store) SaveMCPServer(ctx context.Context, id string, input MCPInput) (domain.MCPServer, error) {
	var err error
	input, err = NormalizeMCPInput(input)
	if err != nil {
		return domain.MCPServer{}, err
	}
	if id == "" {
		id = uuid.NewString()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.MCPServer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var existingEnv, existingHeaders map[string]string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision,env_refs,header_refs FROM mcp_servers WHERE id=$1 FOR UPDATE`, id).Scan(&revision, &existingEnv, &existingHeaders)
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		created = true
		revision = 0
		existingEnv, existingHeaders = map[string]string{}, map[string]string{}
	} else if err != nil {
		return domain.MCPServer{}, err
	}
	envRefs, err := s.upsertSecretMap(ctx, tx, id, "env", "mcp-env", existingEnv, input.Env)
	if err != nil {
		return domain.MCPServer{}, err
	}
	headerRefs, err := s.upsertSecretMap(ctx, tx, id, "header", "mcp-header", existingHeaders, input.Headers)
	if err != nil {
		return domain.MCPServer{}, err
	}
	argsJSON, _ := json.Marshal(input.Args)
	envJSON, _ := json.Marshal(envRefs)
	headerJSON, _ := json.Marshal(headerRefs)
	if input.Provenance == nil {
		input.Provenance = map[string]any{}
	}
	provenanceJSON, err := json.Marshal(input.Provenance)
	if err != nil {
		return domain.MCPServer{}, err
	}
	hash := MCPContentHash(input, envRefs, headerRefs)
	revision++
	revisionID := uuid.NewString()
	if created {
		_, err = tx.Exec(ctx, `INSERT INTO mcp_servers(id,current_revision_id,name,description,revision,transport,command,args,url,env_refs,header_refs,content_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, revisionID, input.Name, strings.TrimSpace(input.Description), revision, input.Transport, input.Command, jsonText(argsJSON), input.URL, jsonText(envJSON), jsonText(headerJSON), hash)
		if err != nil {
			return domain.MCPServer{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO mcp_revisions(id,server_id,revision,name,description,transport,command,args,url,env_slots,header_slots,env_refs,header_refs,content_hash,provenance) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, revisionID, id, revision, input.Name, strings.TrimSpace(input.Description), input.Transport, input.Command, jsonText(argsJSON), input.URL, sortedMapKeys(envRefs), sortedMapKeys(headerRefs), jsonText(envJSON), jsonText(headerJSON), hash, jsonText(provenanceJSON))
	if err != nil {
		return domain.MCPServer{}, err
	}
	if !created {
		_, err = tx.Exec(ctx, `UPDATE mcp_servers SET current_revision_id=$2,name=$3,description=$4,revision=$5,transport=$6,command=$7,args=$8,url=$9,env_refs=$10,header_refs=$11,content_hash=$12,updated_at=now() WHERE id=$1`, id, revisionID, input.Name, strings.TrimSpace(input.Description), revision, input.Transport, input.Command, jsonText(argsJSON), input.URL, jsonText(envJSON), jsonText(headerJSON), hash)
		if err != nil {
			return domain.MCPServer{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.MCPServer{}, err
	}
	return s.MCPServer(ctx, id)
}

func (s *Store) upsertSecretMap(ctx context.Context, tx pgx.Tx, serverID, namespace, kind string, existing, values map[string]string) (map[string]string, error) {
	refs := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("MCP secret key cannot be empty")
		}
		value := values[key]
		if value == "" {
			if ref := existing[key]; ref != "" {
				refs[key] = ref
			}
			continue
		}
		secretID := uuid.NewString()
		ciphertext, err := s.cipher.Encrypt([]byte(value), secretID)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("mcp:%s:%s:%s:%s", serverID, namespace, key, secretID)
		if _, err := tx.Exec(ctx, `INSERT INTO encrypted_secrets(id,name,kind,ciphertext,metadata) VALUES($1,$2,$3,$4,$5)`, secretID, name, kind, ciphertext, `{"writeOnly":true}`); err != nil {
			return nil, err
		}
		refs[key] = secretID
	}
	return refs, nil
}

func MCPContentHash(input MCPInput, envRefs, headerRefs map[string]string) string {
	canonical := struct {
		Name, Transport, Command, URL string
		Args                          []string
		EnvRefs, HeaderRefs           map[string]string
	}{input.Name, input.Transport, input.Command, input.URL, input.Args, envRefs, headerRefs}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) ListMCPServers(ctx context.Context) (json.RawMessage, error) {
	return s.JSONList(ctx, `SELECT id::text,current_revision_id::text AS "currentRevisionId",name,description,revision,transport,command,args,url,(SELECT coalesce(jsonb_agg(key ORDER BY key),'[]'::jsonb) FROM jsonb_object_keys(env_refs) key) AS "envKeys",(SELECT coalesce(jsonb_agg(key ORDER BY key),'[]'::jsonb) FROM jsonb_object_keys(header_refs) key) AS "headerKeys",content_hash AS "contentHash",created_at AS "createdAt",updated_at AS "updatedAt" FROM mcp_servers ORDER BY name,id`)
}

func (s *Store) MCPServer(ctx context.Context, id string) (domain.MCPServer, error) {
	var server domain.MCPServer
	err := s.pool.QueryRow(ctx, `SELECT id::text,current_revision_id::text,name,description,revision,transport,command,args,url,ARRAY(SELECT jsonb_object_keys(env_refs) ORDER BY 1),ARRAY(SELECT jsonb_object_keys(header_refs) ORDER BY 1),content_hash,created_at,updated_at FROM mcp_servers WHERE id=$1`, id).Scan(&server.ID, &server.CurrentRevisionID, &server.Name, &server.Description, &server.Revision, &server.Transport, &server.Command, &server.Args, &server.URL, &server.EnvKeys, &server.HeaderKeys, &server.ContentHash, &server.CreatedAt, &server.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MCPServer{}, ErrNotFound
	}
	return server, err
}

func (s *Store) MCPRevision(ctx context.Context, id string) (domain.MCPRevision, error) {
	var revision domain.MCPRevision
	err := s.pool.QueryRow(ctx, `SELECT id::text,server_id::text,revision,name,description,transport,command,args,url,env_slots,header_slots,content_hash,provenance,created_at FROM mcp_revisions WHERE id=$1`, id).Scan(&revision.ID, &revision.ServerID, &revision.Revision, &revision.Name, &revision.Description, &revision.Transport, &revision.Command, &revision.Args, &revision.URL, &revision.EnvKeys, &revision.HeaderKeys, &revision.ContentHash, &revision.Provenance, &revision.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MCPRevision{}, ErrNotFound
	}
	return revision, err
}

func (s *Store) MCPRevisionHistory(ctx context.Context, serverID string) ([]domain.MCPRevision, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM mcp_revisions WHERE server_id=$1 ORDER BY revision DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.MCPRevision, 0, len(ids))
	for _, id := range ids {
		revision, err := s.MCPRevision(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, revision)
	}
	return result, nil
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	command, err := s.pool.Exec(ctx, `DELETE FROM mcp_servers WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM profile_revision_mcp_servers WHERE server_id=$1)`, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
