package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/jackc/pgx/v5"
)

type RelayConfigurationProjection struct {
	Current          domain.RelayConfigurationRevision `json:"current"`
	Applied          domain.RelayConfigurationRevision `json:"applied"`
	Mode             string                            `json:"mode"`
	DefaultProfileID *string                           `json:"defaultProfileId"`
}

func (s *Store) RelayConfigurationProjection(ctx context.Context) (RelayConfigurationProjection, error) {
	var currentID, appliedID string
	var result RelayConfigurationProjection
	if err := s.pool.QueryRow(ctx, `SELECT current_revision_id::text,applied_revision_id::text,mode,default_profile_id::text FROM relay_configuration_state WHERE singleton`).Scan(&currentID, &appliedID, &result.Mode, &result.DefaultProfileID); err != nil {
		return RelayConfigurationProjection{}, err
	}
	current, err := s.RelayConfiguration(ctx, currentID)
	if err != nil {
		return RelayConfigurationProjection{}, err
	}
	applied, err := s.RelayConfiguration(ctx, appliedID)
	if err != nil {
		return RelayConfigurationProjection{}, err
	}
	result.Current, result.Applied = current, applied
	return result, nil
}

type GlobalPolicyProjection struct {
	Current domain.GlobalPolicyRevision `json:"current"`
	Applied domain.GlobalPolicyRevision `json:"applied"`
}

func (s *Store) GlobalPolicyProjection(ctx context.Context) (GlobalPolicyProjection, error) {
	var currentID, appliedID string
	if err := s.pool.QueryRow(ctx, `SELECT current_revision_id::text,applied_revision_id::text FROM global_policy_state WHERE singleton`).Scan(&currentID, &appliedID); err != nil {
		return GlobalPolicyProjection{}, err
	}
	current, err := s.GlobalPolicy(ctx, currentID)
	if err != nil {
		return GlobalPolicyProjection{}, err
	}
	applied, err := s.GlobalPolicy(ctx, appliedID)
	if err != nil {
		return GlobalPolicyProjection{}, err
	}
	return GlobalPolicyProjection{Current: current, Applied: applied}, nil
}

type ContractRevisionView struct {
	Revision domain.ObservedContractRevision `json:"revision"`
	Tools    []domain.ContractTool           `json:"tools"`
}

type ContractStateView struct {
	ServerID    string                `json:"serverId"`
	ServerName  string                `json:"serverName"`
	ReviewState string                `json:"reviewState"`
	Latest      *ContractRevisionView `json:"latest"`
	Accepted    *ContractRevisionView `json:"accepted"`
}

type ToolRenameProposalView struct {
	ID                        string    `json:"id"`
	ServerID                  string    `json:"serverId"`
	RemovedToolID             string    `json:"removedToolId"`
	RemovedToolName           string    `json:"removedToolName"`
	AddedToolID               string    `json:"addedToolId"`
	AddedToolName             string    `json:"addedToolName"`
	RemovedContractRevisionID string    `json:"removedContractRevisionId"`
	AddedContractRevisionID   string    `json:"addedContractRevisionId"`
	Status                    string    `json:"status"`
	CreatedAt                 time.Time `json:"createdAt"`
}

type ContractGovernanceProjection struct {
	Items   []ContractStateView      `json:"items"`
	Renames []ToolRenameProposalView `json:"renames"`
}

func (s *Store) ContractGovernanceProjection(ctx context.Context) (ContractGovernanceProjection, error) {
	rows, err := s.pool.Query(ctx, `SELECT server.id::text,server.name,coalesce(state.review_state,'unreviewed'),coalesce(state.latest_revision_id::text,''),coalesce(state.accepted_revision_id::text,'') FROM mcp_servers server LEFT JOIN mcp_contract_state state ON state.server_id=server.id ORDER BY server.name,server.id LIMIT 500`)
	if err != nil {
		return ContractGovernanceProjection{}, err
	}
	defer rows.Close()
	result := ContractGovernanceProjection{Items: []ContractStateView{}, Renames: []ToolRenameProposalView{}}
	for rows.Next() {
		var item ContractStateView
		var latestID, acceptedID string
		if err := rows.Scan(&item.ServerID, &item.ServerName, &item.ReviewState, &latestID, &acceptedID); err != nil {
			return ContractGovernanceProjection{}, err
		}
		if latestID != "" {
			revision, err := s.ContractRevisionView(ctx, latestID)
			if err != nil {
				return ContractGovernanceProjection{}, err
			}
			item.Latest = &revision
		}
		if acceptedID != "" {
			revision, err := s.ContractRevisionView(ctx, acceptedID)
			if err != nil {
				return ContractGovernanceProjection{}, err
			}
			item.Accepted = &revision
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ContractGovernanceProjection{}, err
	}
	renameRows, err := s.pool.Query(ctx, `SELECT proposal.id::text,proposal.server_id::text,proposal.removed_tool_id::text,removed.name,proposal.added_tool_id::text,added.name,proposal.removed_contract_revision_id::text,proposal.added_contract_revision_id::text,proposal.status,proposal.created_at FROM mcp_tool_rename_proposals proposal JOIN mcp_tools removed ON removed.id=proposal.removed_tool_id JOIN mcp_tools added ON added.id=proposal.added_tool_id ORDER BY proposal.created_at DESC,proposal.id LIMIT 500`)
	if err != nil {
		return ContractGovernanceProjection{}, err
	}
	defer renameRows.Close()
	for renameRows.Next() {
		var proposal ToolRenameProposalView
		if err := renameRows.Scan(&proposal.ID, &proposal.ServerID, &proposal.RemovedToolID, &proposal.RemovedToolName, &proposal.AddedToolID, &proposal.AddedToolName, &proposal.RemovedContractRevisionID, &proposal.AddedContractRevisionID, &proposal.Status, &proposal.CreatedAt); err != nil {
			return ContractGovernanceProjection{}, err
		}
		result.Renames = append(result.Renames, proposal)
	}
	return result, renameRows.Err()
}

func (s *Store) ContractRevisionView(ctx context.Context, revisionID string) (ContractRevisionView, error) {
	var revision domain.ObservedContractRevision
	var normalized []byte
	err := s.pool.QueryRow(ctx, `SELECT id::text,server_id::text,revision,canonical_hash,normalized_contract,created_at FROM mcp_contract_revisions WHERE id=$1`, revisionID).Scan(&revision.ID, &revision.ServerID, &revision.Revision, &revision.CanonicalHash, &normalized, &revision.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContractRevisionView{}, ErrNotFound
	}
	if err != nil {
		return ContractRevisionView{}, err
	}
	revision.NormalizedContract = json.RawMessage(normalized)
	result := ContractRevisionView{Revision: revision, Tools: []domain.ContractTool{}}
	rows, err := s.pool.Query(ctx, `SELECT tool.id::text,tool.server_id::text,tool.name,member.position,member.input_schema,member.output_schema,member.annotations,member.presentation FROM mcp_contract_revision_tools member JOIN mcp_tools tool ON tool.id=member.tool_id WHERE member.contract_revision_id=$1 ORDER BY member.position`, revisionID)
	if err != nil {
		return ContractRevisionView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var tool domain.ContractTool
		if err := rows.Scan(&tool.ID, &tool.ServerID, &tool.Name, &tool.Position, &tool.InputSchema, &tool.OutputSchema, &tool.Annotations, &tool.Presentation); err != nil {
			return ContractRevisionView{}, err
		}
		result.Tools = append(result.Tools, tool)
	}
	return result, rows.Err()
}
