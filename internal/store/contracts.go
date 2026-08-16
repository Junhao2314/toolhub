package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
	"github.com/Junhao2314/toolhub/internal/domain"
)

const (
	ContractToolUnchanged           = "unchanged"
	ContractToolNewHidden           = "new_hidden"
	ContractToolRemoved             = "removed"
	ContractToolPausedIncompatible  = "paused_incompatible"
	ContractToolChangedPresentation = "changed_presentation"
	maxObservedContractTools        = 500
)

var observedToolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

var forbiddenObservationKeys = map[string]struct{}{
	"secretvalue": {}, "secretvalues": {}, "ciphertext": {}, "arguments": {}, "result": {}, "results": {},
	"prompt": {}, "prompts": {}, "rawerror": {}, "sessionid": {}, "apikey": {},
}

type ObservedToolInput struct {
	Name         string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Annotations  map[string]any
	Presentation map[string]any
	ReadOnlyHint bool
	Mutating     bool
}

type ContractObservationInput struct {
	ServerID string
	Tools    []ObservedToolInput
}

type ContractObservationResult struct {
	Revision domain.ObservedContractRevision
	Statuses map[string]string
}

type RelayContractObservationResult struct {
	Observed int
	Changed  int
	Paused   int
}

type normalizedObservedTool struct {
	Name         string         `json:"name"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  map[string]any `json:"annotations"`
	Presentation map[string]any `json:"presentation"`
}

type normalizedContract struct {
	Tools []normalizedObservedTool `json:"tools"`
}

// CanonicalContract normalizes tool order and JSON object key order. Position
// is assigned only when a revision is stored, so presentation/order changes do
// not create a new contract hash.
func CanonicalContract(tools []ObservedToolInput) (json.RawMessage, string, error) {
	if len(tools) > maxObservedContractTools {
		return nil, "", errors.New("observed contract exceeds tool limit")
	}
	copyTools := append([]ObservedToolInput(nil), tools...)
	sort.Slice(copyTools, func(i, j int) bool { return copyTools[i].Name < copyTools[j].Name })
	normalized := normalizedContract{Tools: make([]normalizedObservedTool, 0, len(copyTools))}
	seen := make(map[string]struct{}, len(copyTools))
	for _, tool := range copyTools {
		name := strings.TrimSpace(tool.Name)
		if !observedToolNamePattern.MatchString(name) {
			return nil, "", fmt.Errorf("invalid observed tool name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, "", fmt.Errorf("duplicate observed tool %q", name)
		}
		seen[name] = struct{}{}
		input, err := normalizeObject(tool.InputSchema)
		if err != nil {
			return nil, "", fmt.Errorf("tool %s input schema: %w", name, err)
		}
		output, err := normalizeObject(tool.OutputSchema)
		if err != nil {
			return nil, "", fmt.Errorf("tool %s output schema: %w", name, err)
		}
		annotations := cloneObject(tool.Annotations)
		if tool.ReadOnlyHint {
			annotations["readOnlyHint"] = true
		}
		if tool.Mutating {
			annotations["mutatingHint"] = true
		}
		if err := rejectForbiddenObservationKeys(annotations); err != nil {
			return nil, "", fmt.Errorf("tool %s annotations: %w", name, err)
		}
		presentation := cloneObject(tool.Presentation)
		if err := rejectForbiddenObservationKeys(presentation); err != nil {
			return nil, "", fmt.Errorf("tool %s presentation: %w", name, err)
		}
		normalized.Tools = append(normalized.Tools, normalizedObservedTool{
			Name: name, InputSchema: input, OutputSchema: output,
			Annotations: annotations, Presentation: presentation,
		})
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return json.RawMessage(body), hex.EncodeToString(sum[:]), nil
}

func (s *Store) ObserveContracts(ctx context.Context, input ContractObservationInput) (ContractObservationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ContractObservationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.ObserveContractsTx(ctx, tx, input)
	if err != nil {
		return ContractObservationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ContractObservationResult{}, err
	}
	return result, nil
}

func (s *Store) ObserveRelayContracts(ctx context.Context, response bridgeprotocol.ContractObservationResponse) (RelayContractObservationResult, error) {
	if uuid.Validate(response.RelayConfigurationRevisionID) != nil || len(response.Servers) > 500 {
		return RelayContractObservationResult{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RelayContractObservationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var appliedRevisionID string
	if err := tx.QueryRow(ctx, `SELECT applied_revision_id::text FROM relay_configuration_state WHERE singleton FOR SHARE`).Scan(&appliedRevisionID); err != nil {
		return RelayContractObservationResult{}, err
	}
	if appliedRevisionID != response.RelayConfigurationRevisionID {
		return RelayContractObservationResult{}, ErrConflict
	}
	type expectedServer struct{ name, revisionID string }
	expected := map[string]expectedServer{}
	rows, err := tx.Query(ctx, `SELECT member.server_id::text,server.name,member.mcp_revision_id::text FROM relay_configuration_revision_mcp_servers member JOIN mcp_servers server ON server.id=member.server_id WHERE member.relay_configuration_revision_id=$1 ORDER BY member.position`, appliedRevisionID)
	if err != nil {
		return RelayContractObservationResult{}, err
	}
	for rows.Next() {
		var serverID string
		var item expectedServer
		if err := rows.Scan(&serverID, &item.name, &item.revisionID); err != nil {
			rows.Close()
			return RelayContractObservationResult{}, err
		}
		expected[serverID] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RelayContractObservationResult{}, err
	}
	rows.Close()
	if len(expected) != len(response.Servers) {
		return RelayContractObservationResult{}, ErrConflict
	}

	result := RelayContractObservationResult{}
	seen := map[string]struct{}{}
	for _, observed := range response.Servers {
		server, ok := expected[observed.ServerID]
		if !ok || server.name != observed.ServerName || server.revisionID != observed.MCPConfigRevisionID {
			return RelayContractObservationResult{}, ErrConflict
		}
		if _, duplicate := seen[observed.ServerID]; duplicate {
			return RelayContractObservationResult{}, ErrConflict
		}
		seen[observed.ServerID] = struct{}{}
		tools := make([]ObservedToolInput, 0, len(observed.Tools))
		for _, tool := range observed.Tools {
			inputSchema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return RelayContractObservationResult{}, err
			}
			outputSchema, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				return RelayContractObservationResult{}, err
			}
			presentation := map[string]any{}
			if tool.Title != nil {
				presentation["title"] = *tool.Title
			}
			if tool.Description != nil {
				presentation["description"] = *tool.Description
			}
			readOnly, _ := tool.Annotations["readOnlyHint"].(bool)
			mutating, _ := tool.Annotations["mutatingHint"].(bool)
			tools = append(tools, ObservedToolInput{Name: tool.Name, InputSchema: inputSchema, OutputSchema: outputSchema, Annotations: cloneObject(tool.Annotations), Presentation: presentation, ReadOnlyHint: readOnly, Mutating: mutating})
		}
		observation, err := s.ObserveContractsTx(ctx, tx, ContractObservationInput{ServerID: observed.ServerID, Tools: tools})
		if err != nil {
			return RelayContractObservationResult{}, err
		}
		result.Observed++
		for _, status := range observation.Statuses {
			if status != ContractToolUnchanged {
				result.Changed++
			}
			if status == ContractToolPausedIncompatible {
				result.Paused++
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RelayContractObservationResult{}, err
	}
	return result, nil
}

func (s *Store) ObserveContractsTx(ctx context.Context, tx pgx.Tx, input ContractObservationInput) (ContractObservationResult, error) {
	if uuid.Validate(input.ServerID) != nil {
		return ContractObservationResult{}, ErrNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_servers WHERE id=$1)`, input.ServerID).Scan(&exists); err != nil {
		return ContractObservationResult{}, err
	}
	if !exists {
		return ContractObservationResult{}, ErrNotFound
	}
	body, hash, err := CanonicalContract(input.Tools)
	if err != nil {
		return ContractObservationResult{}, err
	}
	var latestID, acceptedID, reviewState string
	err = tx.QueryRow(ctx, `SELECT coalesce(latest_revision_id::text,''),coalesce(accepted_revision_id::text,''),review_state FROM mcp_contract_state WHERE server_id=$1 FOR UPDATE`, input.ServerID).Scan(&latestID, &acceptedID, &reviewState)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_contract_state(server_id) VALUES($1)`, input.ServerID); err != nil {
			return ContractObservationResult{}, err
		}
		reviewState = "unreviewed"
	} else if err != nil {
		return ContractObservationResult{}, err
	}
	if latestID != "" {
		var latestHash string
		if err := tx.QueryRow(ctx, `SELECT canonical_hash FROM mcp_contract_revisions WHERE id=$1`, latestID).Scan(&latestHash); err != nil {
			return ContractObservationResult{}, err
		}
		if latestHash == hash {
			revision, err := contractRevisionTx(ctx, tx, latestID)
			if err != nil {
				return ContractObservationResult{}, err
			}
			baselineID := acceptedID
			if baselineID == "" {
				baselineID = latestID
			}
			baselineTools, err := contractToolsTx(ctx, tx, baselineID)
			if err != nil {
				return ContractObservationResult{}, err
			}
			return ContractObservationResult{Revision: revision, Statuses: compareContractTools(baselineTools, input.Tools)}, nil
		}
	}
	previousTools, err := contractToolsTx(ctx, tx, latestID)
	if err != nil {
		return ContractObservationResult{}, err
	}
	baselineTools := previousTools
	if acceptedID != "" && acceptedID != latestID {
		baselineTools, err = contractToolsTx(ctx, tx, acceptedID)
		if err != nil {
			return ContractObservationResult{}, err
		}
	}
	var revisionNumber int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0)+1 FROM mcp_contract_revisions WHERE server_id=$1`, input.ServerID).Scan(&revisionNumber); err != nil {
		return ContractObservationResult{}, err
	}
	revisionID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_contract_revisions(id,server_id,revision,canonical_hash,normalized_contract) VALUES($1,$2,$3,$4,$5)`, revisionID, input.ServerID, revisionNumber, hash, jsonText(body)); err != nil {
		return ContractObservationResult{}, err
	}
	statuses := compareContractTools(baselineTools, input.Tools)
	for position, tool := range normalizedToolsFromBody(body) {
		toolID, err := stableContractToolID(ctx, tx, input.ServerID, tool.Name)
		if err != nil {
			return ContractObservationResult{}, err
		}
		toolBody, err := json.Marshal(tool)
		if err != nil {
			return ContractObservationResult{}, err
		}
		var normalized map[string]any
		if err := json.Unmarshal(toolBody, &normalized); err != nil {
			return ContractObservationResult{}, err
		}
		inputJSON, _ := json.Marshal(normalized["inputSchema"])
		outputJSON, _ := json.Marshal(normalized["outputSchema"])
		annotationsJSON, _ := json.Marshal(normalized["annotations"])
		presentationJSON, _ := json.Marshal(normalized["presentation"])
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_contract_revision_tools(contract_revision_id,tool_id,position,input_schema,output_schema,annotations,presentation,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, revisionID, toolID, position, jsonText(inputJSON), jsonText(outputJSON), jsonText(annotationsJSON), jsonText(presentationJSON), statuses[tool.Name]); err != nil {
			return ContractObservationResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_contract_revision_seals(contract_revision_id) VALUES($1)`, revisionID); err != nil {
		return ContractObservationResult{}, err
	}
	newState := "changed"
	for _, status := range statuses {
		if status == ContractToolPausedIncompatible {
			newState = "paused"
			break
		}
	}
	if latestID == "" {
		newState = "unreviewed"
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_contract_state SET latest_revision_id=$2,review_state=$3,updated_at=now() WHERE server_id=$1`, input.ServerID, revisionID, newState); err != nil {
		return ContractObservationResult{}, err
	}
	renameStatuses := compareContractTools(previousTools, input.Tools)
	if err := createRenameProposalTx(ctx, tx, input.ServerID, latestID, revisionID, previousTools, input.Tools, renameStatuses); err != nil {
		return ContractObservationResult{}, err
	}
	revision, err := contractRevisionTx(ctx, tx, revisionID)
	if err != nil {
		return ContractObservationResult{}, err
	}
	return ContractObservationResult{Revision: revision, Statuses: statuses}, nil
}

func (s *Store) AcceptContract(ctx context.Context, serverID, revisionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var owner string
	if err := tx.QueryRow(ctx, `SELECT server_id::text FROM mcp_contract_revisions WHERE id=$1 FOR SHARE`, revisionID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if owner != serverID {
		return ErrConflict
	}
	var mode, relayRevisionID, legacyProfileID, legacyProfileState string
	if err := tx.QueryRow(ctx, `SELECT mode,applied_revision_id::text,coalesce(legacy_profile_id::text,''),legacy_profile_state FROM relay_configuration_state WHERE singleton FOR SHARE`).Scan(&mode, &relayRevisionID, &legacyProfileID, &legacyProfileState); err != nil {
		return err
	}
	var previousAcceptedRevisionID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(accepted_revision_id::text,'') FROM mcp_contract_state WHERE server_id=$1 FOR UPDATE`, serverID).Scan(&previousAcceptedRevisionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_contract_state SET accepted_revision_id=$2,review_state='accepted',updated_at=now() WHERE server_id=$1`, serverID, revisionID); err != nil {
		return err
	}
	if previousAcceptedRevisionID == "" {
		if err := s.bootstrapFirstContractProfilesTx(ctx, tx, serverID, mode, relayRevisionID, legacyProfileID, legacyProfileState); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) bootstrapFirstContractProfilesTx(ctx context.Context, tx pgx.Tx, acceptedServerID, mode, relayRevisionID, legacyProfileID, legacyProfileState string) error {
	if mode != "compatibility" || legacyProfileID == "" || legacyProfileState != "pending" {
		return nil
	}
	type relayPin struct {
		serverID, mcpRevisionID, acceptedContractRevisionID string
	}
	relayPins := []relayPin{}
	containsAcceptedServer := false
	rows, err := tx.Query(ctx, `
		SELECT member.server_id::text,member.mcp_revision_id::text,coalesce(contract.accepted_revision_id::text,'')
		FROM relay_configuration_revision_mcp_servers member
		LEFT JOIN mcp_contract_state contract ON contract.server_id=member.server_id
		WHERE member.relay_configuration_revision_id=$1
		ORDER BY member.position`, relayRevisionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pin relayPin
		if err := rows.Scan(&pin.serverID, &pin.mcpRevisionID, &pin.acceptedContractRevisionID); err != nil {
			rows.Close()
			return err
		}
		relayPins = append(relayPins, pin)
		containsAcceptedServer = containsAcceptedServer || pin.serverID == acceptedServerID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !containsAcceptedServer {
		return nil
	}

	type profileCandidate struct{ profileID, revisionID string }
	profiles := []profileCandidate{}
	rows, err = tx.Query(ctx, `
		SELECT id::text,current_revision_id::text
		FROM profiles
		WHERE archived_at IS NULL
		  AND client_kind IN ('claude','codex')
		  AND migration_state='ready'
		ORDER BY id
		FOR UPDATE`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var candidate profileCandidate
		if err := rows.Scan(&candidate.profileID, &candidate.revisionID); err != nil {
			rows.Close()
			return err
		}
		profiles = append(profiles, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, candidate := range profiles {
		input, err := profileInputFromRevisionTx(ctx, tx, candidate.profileID, candidate.revisionID)
		if err != nil {
			return err
		}
		if input.PendingBindings {
			var applicableBindings int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM pending_secret_bindings binding JOIN relay_configuration_revision_mcp_servers member ON member.relay_configuration_revision_id=$2 AND member.mcp_revision_id=binding.mcp_revision_id WHERE binding.profile_revision_id=$1`, candidate.revisionID, relayRevisionID).Scan(&applicableBindings); err != nil {
				return err
			}
			if applicableBindings == 0 {
				continue
			}
		}
		input.MCPServerIDs = make([]string, 0, len(relayPins))
		input.MCPRevisionIDs = make(map[string]string, len(relayPins))
		input.MCPGovernance = make([]ProfileMCPGovernanceInput, 0, len(relayPins))
		for _, pin := range relayPins {
			input.MCPServerIDs = append(input.MCPServerIDs, pin.serverID)
			input.MCPRevisionIDs[pin.serverID] = pin.mcpRevisionID
			input.MCPGovernance = append(input.MCPGovernance, ProfileMCPGovernanceInput{
				ServerID: pin.serverID, MCPRevisionID: pin.mcpRevisionID,
				AcceptedContractRevisionID: pin.acceptedContractRevisionID,
				VisibilityMode:             "all_accepted",
			})
		}
		_, newRevisionID, err := s.saveProfileTx(ctx, tx, candidate.profileID, input)
		if err != nil {
			return err
		}
		if input.PendingBindings {
			if _, err := tx.Exec(ctx, `
				INSERT INTO pending_secret_bindings(profile_revision_id,mcp_revision_id,namespace,key,slot_hash,secret_id,bound_at)
				SELECT $1,binding.mcp_revision_id,binding.namespace,binding.key,binding.slot_hash,binding.secret_id,binding.bound_at
				FROM pending_secret_bindings binding
				JOIN profile_revision_mcp_servers member ON member.profile_revision_id=$1 AND member.mcp_revision_id=binding.mcp_revision_id
				WHERE binding.profile_revision_id=$2`, newRevisionID, candidate.revisionID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ConfirmToolRename records an operator-confirmed identity relationship and
// creates draft governance revisions. Published profile pointers and the
// applied global policy remain unchanged until a later Apply operation.
func (s *Store) ConfirmToolRename(ctx context.Context, proposalID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var serverID, oldToolID, newToolID, oldContractID, newContractID, status string
	if err := tx.QueryRow(ctx, `SELECT server_id::text,removed_tool_id::text,added_tool_id::text,removed_contract_revision_id::text,added_contract_revision_id::text,status FROM mcp_tool_rename_proposals WHERE id=$1 FOR UPDATE`, proposalID).Scan(&serverID, &oldToolID, &newToolID, &oldContractID, &newContractID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != "suspected" && status != "ambiguous" {
		return ErrConflict
	}
	if err := validateRenameProposalTx(ctx, tx, serverID, oldToolID, newToolID, oldContractID, newContractID); err != nil {
		return err
	}
	if status == "ambiguous" {
		if _, err := tx.Exec(ctx, `
			UPDATE mcp_tool_rename_proposals
			SET status='rejected'
			WHERE id<>$1
			  AND server_id=$2
			  AND removed_contract_revision_id=$3
			  AND added_contract_revision_id=$4
			  AND status='ambiguous'
			  AND (removed_tool_id=$5 OR added_tool_id=$6)`, proposalID, serverID, oldContractID, newContractID, oldToolID, newToolID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_tool_renames(id,server_id,old_tool_id,new_tool_id,confirmed_removed_contract_revision_id,confirmed_added_contract_revision_id) VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), serverID, oldToolID, newToolID, oldContractID, newContractID); err != nil {
		return err
	}
	if err := cloneGlobalPolicyForRenameTx(ctx, tx, oldToolID, newToolID); err != nil {
		return err
	}
	if err := cloneProfilesForRenameTx(ctx, tx, serverID, oldToolID, newToolID, oldContractID, newContractID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_tool_rename_proposals SET status='confirmed' WHERE id=$1`, proposalID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mcp_contract_state SET accepted_revision_id=$2,review_state='accepted',updated_at=now() WHERE server_id=$1`, serverID, newContractID); err != nil {
		return err
	}
	auditMetadata, _ := json.Marshal(map[string]string{"oldToolId": oldToolID, "newToolId": newToolID, "oldContractRevisionId": oldContractID, "newContractRevisionId": newContractID})
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,action,resource_type,resource_id,outcome,metadata) VALUES($1,'mcp_tool_rename_confirmed','mcp_server',$2,'success',$3)`, uuid.NewString(), serverID, jsonText(auditMetadata)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateRenameProposalTx(ctx context.Context, tx pgx.Tx, serverID, oldToolID, newToolID, oldContractID, newContractID string) error {
	for _, toolID := range []string{oldToolID, newToolID} {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT server_id::text FROM mcp_tools WHERE id=$1 FOR UPDATE`, toolID).Scan(&owner); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if owner != serverID {
			return ErrConflict
		}
	}
	for _, contractID := range []string{oldContractID, newContractID} {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT server_id::text FROM mcp_contract_revisions WHERE id=$1 FOR UPDATE`, contractID).Scan(&owner); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if owner != serverID {
			return ErrConflict
		}
	}
	for _, pair := range []struct{ contractID, toolID string }{{oldContractID, oldToolID}, {newContractID, newToolID}} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_contract_revision_tools WHERE contract_revision_id=$1 AND tool_id=$2)`, pair.contractID, pair.toolID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrConflict
		}
	}
	var latestID string
	if err := tx.QueryRow(ctx, `SELECT coalesce(latest_revision_id::text,'') FROM mcp_contract_state WHERE server_id=$1 FOR UPDATE`, serverID).Scan(&latestID); err != nil {
		return err
	}
	if latestID != newContractID {
		return ErrConflict
	}
	return nil
}

func cloneGlobalPolicyForRenameTx(ctx context.Context, tx pgx.Tx, oldToolID, newToolID string) error {
	var currentID string
	if err := tx.QueryRow(ctx, `SELECT current_revision_id::text FROM global_policy_state WHERE singleton FOR UPDATE`).Scan(&currentID); err != nil {
		return err
	}
	var input GlobalPolicyInput
	var overrides []byte
	if err := tx.QueryRow(ctx, `SELECT revision,catalog_version,explicit_overrides,unclassified_mutating,reviewed_read_only FROM global_policy_revisions WHERE id=$1`, currentID).Scan(&input.Revision, &input.CatalogVersion, &overrides, &input.UnclassifiedMutating, &input.ReviewedReadOnly); err != nil {
		return err
	}
	if err := json.Unmarshal(overrides, &input.ExplicitOverrides); err != nil {
		return err
	}
	if decision := input.ExplicitOverrides[oldToolID]; decision != "" {
		delete(input.ExplicitOverrides, oldToolID)
		input.ExplicitOverrides[newToolID] = decision
	} else {
		return nil
	}
	hash, err := canonicalPolicyHash(input)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	nextRevision := input.Revision + 1
	overrideJSON, _ := json.Marshal(input.ExplicitOverrides)
	if _, err := tx.Exec(ctx, `INSERT INTO global_policy_revisions(id,revision,canonical_hash,catalog_version,explicit_overrides,unclassified_mutating,reviewed_read_only) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, nextRevision, hash, input.CatalogVersion, jsonText(overrideJSON), input.UnclassifiedMutating, input.ReviewedReadOnly); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE global_policy_state SET current_revision_id=$1,updated_at=now() WHERE singleton`, id)
	return err
}

func cloneProfilesForRenameTx(ctx context.Context, tx pgx.Tx, serverID, oldToolID, newToolID, oldContractID, newContractID string) error {
	rows, err := tx.Query(ctx, `
		SELECT p.id::text,p.current_revision_id::text
		FROM profiles p
		WHERE EXISTS (
			SELECT 1 FROM profile_revision_tool_rules r
			WHERE r.profile_revision_id=p.current_revision_id AND r.tool_id=$1
		) OR EXISTS (
			SELECT 1 FROM profile_revision_mcp_governance g
			WHERE g.profile_revision_id=p.current_revision_id
			  AND g.server_id=$2
			  AND g.accepted_contract_revision_id=$3
			  AND g.visibility_mode='all_accepted'
		)
		ORDER BY p.id FOR UPDATE OF p`, oldToolID, serverID, oldContractID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type profileCandidate struct{ profileID, revisionID string }
	var candidates []profileCandidate
	for rows.Next() {
		var candidate profileCandidate
		if err := rows.Scan(&candidate.profileID, &candidate.revisionID); err != nil {
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, candidate := range candidates {
		var revision int64
		var name, description, clientKind, category, variant, migrationState string
		var pending, archivedRestore bool
		if err := tx.QueryRow(ctx, `SELECT revision,name,description,client_kind,category,variant,migration_state,pending_bindings,archived_restore FROM profile_revisions WHERE id=$1`, candidate.revisionID).Scan(&revision, &name, &description, &clientKind, &category, &variant, &migrationState, &pending, &archivedRestore); err != nil {
			return err
		}
		newRevisionID := uuid.NewString()
		candidateHash, err := canonicalRenamedProfileHashTx(ctx, tx, candidate.revisionID, ProfileInput{Name: name, Description: description, ClientKind: clientKind, Category: category, Variant: variant, MigrationState: migrationState}, oldToolID, newToolID, oldContractID, newContractID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revisions(id,profile_id,revision,name,description,client_kind,category,variant,migration_state,canonical_hash,pending_bindings,archived_restore) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, newRevisionID, candidate.profileID, revision+1, name, description, clientKind, category, variant, migrationState, candidateHash, pending, archivedRestore); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_skills(profile_revision_id,skill_id,skill_version_id,position) SELECT $1,skill_id,skill_version_id,position FROM profile_revision_skills WHERE profile_revision_id=$2`, newRevisionID, candidate.revisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_mcp_servers(profile_revision_id,server_id,mcp_revision_id,position) SELECT $1,server_id,mcp_revision_id,position FROM profile_revision_mcp_servers WHERE profile_revision_id=$2`, newRevisionID, candidate.revisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_mcp_governance(profile_revision_id,server_id,mcp_revision_id,accepted_contract_revision_id,visibility_mode) SELECT $1,server_id,mcp_revision_id,CASE WHEN accepted_contract_revision_id=$3 THEN $4 ELSE accepted_contract_revision_id END,visibility_mode FROM profile_revision_mcp_governance WHERE profile_revision_id=$2`, newRevisionID, candidate.revisionID, oldContractID, newContractID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_tool_rules(profile_revision_id,tool_id,visible,decision,reason_codes) SELECT $1,CASE WHEN tool_id=$3 THEN $4 ELSE tool_id END,visible,decision,reason_codes FROM profile_revision_tool_rules WHERE profile_revision_id=$2`, newRevisionID, candidate.revisionID, oldToolID, newToolID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pending_secret_bindings(profile_revision_id,mcp_revision_id,namespace,key,slot_hash,secret_id,bound_at) SELECT $1,mcp_revision_id,namespace,key,slot_hash,secret_id,bound_at FROM pending_secret_bindings WHERE profile_revision_id=$2`, newRevisionID, candidate.revisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile_revision_seals(profile_revision_id) VALUES($1)`, newRevisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE profiles SET revision=$2,current_revision_id=$3,updated_at=now() WHERE id=$1`, candidate.profileID, revision+1, newRevisionID); err != nil {
			return err
		}
	}
	return nil
}

func canonicalRenamedProfileHashTx(ctx context.Context, tx pgx.Tx, revisionID string, input ProfileInput, oldToolID, newToolID, oldContractID, newContractID string) (string, error) {
	skills := []domain.ProfileSkillPin{}
	rows, err := tx.Query(ctx, `SELECT prs.skill_id::text,prs.skill_version_id::text,sk.slug,sk.name,a.canonical_sha256,a.content_hash,prs.skill_version_id=sk.current_version_id FROM profile_revision_skills prs JOIN skills sk ON sk.id=prs.skill_id JOIN skill_versions sv ON sv.id=prs.skill_version_id JOIN skill_artifacts a ON a.id=sv.artifact_id WHERE prs.profile_revision_id=$1 ORDER BY prs.position`, revisionID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var pin domain.ProfileSkillPin
		if err := rows.Scan(&pin.SkillID, &pin.VersionID, &pin.Slug, &pin.Name, &pin.SHA256, &pin.ContentHash, &pin.Current); err != nil {
			rows.Close()
			return "", err
		}
		skills = append(skills, pin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	servers := []domain.ProfileMCPPin{}
	rows, err = tx.Query(ctx, `SELECT prms.server_id::text,prms.mcp_revision_id::text,mr.revision,mr.name,mr.description,mr.transport,mr.command,mr.args,mr.url,mr.env_slots,mr.header_slots,mr.content_hash,prms.mcp_revision_id=ms.current_revision_id FROM profile_revision_mcp_servers prms JOIN mcp_revisions mr ON mr.id=prms.mcp_revision_id JOIN mcp_servers ms ON ms.id=prms.server_id WHERE prms.profile_revision_id=$1 ORDER BY prms.position`, revisionID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var pin domain.ProfileMCPPin
		if err := rows.Scan(&pin.ServerID, &pin.RevisionID, &pin.Revision, &pin.Name, &pin.Description, &pin.Transport, &pin.Command, &pin.Args, &pin.URL, &pin.EnvKeys, &pin.HeaderKeys, &pin.ContentHash, &pin.Current); err != nil {
			rows.Close()
			return "", err
		}
		servers = append(servers, pin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT server_id::text,mcp_revision_id::text,coalesce(accepted_contract_revision_id::text,''),visibility_mode FROM profile_revision_mcp_governance WHERE profile_revision_id=$1 ORDER BY server_id`, revisionID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var item ProfileMCPGovernanceInput
		if err := rows.Scan(&item.ServerID, &item.MCPRevisionID, &item.AcceptedContractRevisionID, &item.VisibilityMode); err != nil {
			rows.Close()
			return "", err
		}
		if item.AcceptedContractRevisionID == oldContractID {
			item.AcceptedContractRevisionID = newContractID
		}
		input.MCPGovernance = append(input.MCPGovernance, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT tool_id::text,visible,decision,reason_codes FROM profile_revision_tool_rules WHERE profile_revision_id=$1 ORDER BY tool_id`, revisionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var item ProfileToolRuleInput
		if err := rows.Scan(&item.ToolID, &item.Visible, &item.Decision, &item.ReasonCodes); err != nil {
			return "", err
		}
		if item.ToolID == oldToolID {
			item.ToolID = newToolID
		}
		input.ToolRules = append(input.ToolRules, item)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return CanonicalGovernedProfileHash(input, skills, servers)
}

type storedContractTool struct {
	ID           string
	Name         string
	InputSchema  map[string]any
	OutputSchema map[string]any
	Annotations  map[string]any
	Presentation map[string]any
}

func contractToolsTx(ctx context.Context, tx pgx.Tx, revisionID string) (map[string]storedContractTool, error) {
	result := map[string]storedContractTool{}
	if revisionID == "" {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.name,crt.input_schema,crt.output_schema,crt.annotations,crt.presentation FROM mcp_contract_revision_tools crt JOIN mcp_tools t ON t.id=crt.tool_id WHERE crt.contract_revision_id=$1`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item storedContractTool
		var input, output, annotations, presentation []byte
		if err := rows.Scan(&item.ID, &item.Name, &input, &output, &annotations, &presentation); err != nil {
			return nil, err
		}
		for raw, target := range map[string][]byte{"input": input, "output": output, "annotations": annotations, "presentation": presentation} {
			var value map[string]any
			if len(target) > 0 {
				if err := json.Unmarshal(target, &value); err != nil {
					return nil, err
				}
			}
			switch raw {
			case "input":
				item.InputSchema = value
			case "output":
				item.OutputSchema = value
			case "annotations":
				item.Annotations = value
			case "presentation":
				item.Presentation = value
			}
		}
		result[item.Name] = item
	}
	return result, rows.Err()
}

func contractRevisionTx(ctx context.Context, tx pgx.Tx, id string) (domain.ObservedContractRevision, error) {
	var revision domain.ObservedContractRevision
	var body []byte
	if err := tx.QueryRow(ctx, `SELECT id::text,server_id::text,revision,canonical_hash,normalized_contract,created_at FROM mcp_contract_revisions WHERE id=$1`, id).Scan(&revision.ID, &revision.ServerID, &revision.Revision, &revision.CanonicalHash, &body, &revision.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ObservedContractRevision{}, ErrNotFound
		}
		return domain.ObservedContractRevision{}, err
	}
	revision.NormalizedContract = json.RawMessage(body)
	return revision, nil
}

func stableContractToolID(ctx context.Context, tx pgx.Tx, serverID, name string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM mcp_tools WHERE server_id=$1 AND name=$2`, serverID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id = uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO mcp_tools(id,server_id,name) VALUES($1,$2,$3)`, id, serverID, name); err != nil {
		return "", err
	}
	return id, nil
}

func normalizedToolsFromBody(body json.RawMessage) []normalizedObservedTool {
	var value normalizedContract
	_ = json.Unmarshal(body, &value)
	return value.Tools
}

func unchangedStatuses(tools []ObservedToolInput) map[string]string {
	result := make(map[string]string, len(tools))
	for _, tool := range tools {
		result[tool.Name] = ContractToolUnchanged
	}
	return result
}

func compareContractTools(previous map[string]storedContractTool, current []ObservedToolInput) map[string]string {
	result := make(map[string]string, len(previous)+len(current))
	for name := range previous {
		result[name] = ContractToolRemoved
	}
	for _, tool := range current {
		old, exists := previous[tool.Name]
		if !exists {
			result[tool.Name] = ContractToolNewHidden
			continue
		}
		input, _ := normalizeObject(tool.InputSchema)
		output, _ := normalizeObject(tool.OutputSchema)
		annotations := cloneObject(tool.Annotations)
		if tool.ReadOnlyHint {
			annotations["readOnlyHint"] = true
		}
		if tool.Mutating {
			annotations["mutatingHint"] = true
		}
		if !equalJSONMap(old.InputSchema, input) || !equalJSONMap(old.OutputSchema, output) || !equalJSONMap(old.Annotations, annotations) {
			result[tool.Name] = ContractToolPausedIncompatible
		} else if !equalJSONMap(old.Presentation, cloneObject(tool.Presentation)) {
			result[tool.Name] = ContractToolChangedPresentation
		} else {
			result[tool.Name] = ContractToolUnchanged
		}
	}
	return result
}

func createRenameProposalTx(ctx context.Context, tx pgx.Tx, serverID, oldRevisionID, newRevisionID string, previous map[string]storedContractTool, current []ObservedToolInput, statuses map[string]string) error {
	if oldRevisionID == "" || len(previous) == 0 {
		return nil
	}
	removed := make([]storedContractTool, 0, 2)
	added := make([]ObservedToolInput, 0, 2)
	for name, tool := range previous {
		if statuses[name] == ContractToolRemoved {
			removed = append(removed, tool)
		}
	}
	for _, tool := range current {
		if statuses[tool.Name] == ContractToolNewHidden {
			added = append(added, tool)
		}
	}
	if len(removed) == 1 && len(added) == 1 {
		newToolID, err := toolIDForRevisionTx(ctx, tx, serverID, newRevisionID, added[0].Name)
		if err != nil {
			return err
		}
		if schemaPairEqual(removed[0], added[0]) {
			_, err = tx.Exec(ctx, `INSERT INTO mcp_tool_rename_proposals(id,server_id,removed_tool_id,added_tool_id,removed_contract_revision_id,added_contract_revision_id,status) VALUES($1,$2,$3,$4,$5,$6,'suspected') ON CONFLICT DO NOTHING`, uuid.NewString(), serverID, removed[0].ID, newToolID, oldRevisionID, newRevisionID)
			return err
		}
	}
	if len(removed) > 0 && len(added) > 0 {
		for _, old := range removed {
			for _, candidate := range added {
				if schemaPairEqual(old, candidate) {
					newToolID, err := toolIDForRevisionTx(ctx, tx, serverID, newRevisionID, candidate.Name)
					if err != nil {
						return err
					}
					_, err = tx.Exec(ctx, `INSERT INTO mcp_tool_rename_proposals(id,server_id,removed_tool_id,added_tool_id,removed_contract_revision_id,added_contract_revision_id,status) VALUES($1,$2,$3,$4,$5,$6,'ambiguous') ON CONFLICT DO NOTHING`, uuid.NewString(), serverID, old.ID, newToolID, oldRevisionID, newRevisionID)
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func toolIDForRevisionTx(ctx context.Context, tx pgx.Tx, serverID, revisionID, name string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT t.id::text FROM mcp_contract_revision_tools crt JOIN mcp_tools t ON t.id=crt.tool_id WHERE crt.contract_revision_id=$1 AND t.server_id=$2 AND t.name=$3`, revisionID, serverID, name).Scan(&id)
	return id, err
}

func schemaPairEqual(old storedContractTool, added ObservedToolInput) bool {
	input, _ := normalizeObject(added.InputSchema)
	output, _ := normalizeObject(added.OutputSchema)
	return equalJSONMap(old.InputSchema, input) && equalJSONMap(old.OutputSchema, output)
}

func normalizeObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func cloneObject(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	body, _ := json.Marshal(value)
	var copy map[string]any
	_ = json.Unmarshal(body, &copy)
	if copy == nil {
		return map[string]any{}
	}
	return copy
}

func rejectForbiddenObservationKeys(value any) error {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if _, forbidden := forbiddenObservationKeys[normalizedGovernanceMetadataKey(key)]; forbidden {
				return fmt.Errorf("forbidden observation field %q", key)
			}
			if err := rejectForbiddenObservationKeys(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range item {
			if err := rejectForbiddenObservationKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func equalJSONMap(left, right map[string]any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return string(a) == string(b)
}
