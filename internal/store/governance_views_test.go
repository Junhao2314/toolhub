package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestContractGovernanceProjectionIsBounded(t *testing.T) {
	t.Run("servers", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		tx, err := st.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			INSERT INTO mcp_servers(id,name,transport,url,content_hash,current_revision_id)
			SELECT md5('bounded-server-'||value)::uuid,
			       'bounded-'||lpad(value::text,3,'0'),
			       'http','https://example.invalid/'||value,repeat('a',64),
			       md5('bounded-server-'||value)::uuid
			FROM generate_series(1,501) value`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO mcp_revisions(id,server_id,revision,name,transport,url,content_hash)
			SELECT id,id,1,name,transport,url,content_hash
			FROM mcp_servers WHERE name LIKE 'bounded-%'`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		projection, err := st.ContractGovernanceProjection(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.Items) != 500 {
			t.Fatalf("server projection length=%d, want 500", len(projection.Items))
		}
		if got := projection.Items[499].ServerName; got != "bounded-500" {
			t.Fatalf("last bounded server=%q, want bounded-500", got)
		}
	})

	t.Run("rename proposals", func(t *testing.T) {
		ctx := context.Background()
		st := newIntegrationStore(t, true)
		server, err := st.SaveMCPServer(ctx, "", MCPInput{Name: "bounded-renames", Transport: "http", URL: "https://example.invalid/mcp"})
		if err != nil {
			t.Fatal(err)
		}
		removedContractID, addedContractID := uuid.NewString(), uuid.NewString()
		tx, err := st.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_contract_revisions(id,server_id,revision,canonical_hash,normalized_contract) VALUES($1,$3,1,$4,'{"tools":[]}'),($2,$3,2,$5,'{"tools":[]}')`, removedContractID, addedContractID, server.ID, fmt.Sprintf("%064x", 1), fmt.Sprintf("%064x", 2)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mcp_contract_revision_seals(contract_revision_id) VALUES($1),($2)`, removedContractID, addedContractID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `
			INSERT INTO mcp_tools(id,server_id,name)
			SELECT md5('bounded-removed-'||value)::uuid,$1::uuid,'removed'||lpad(value::text,3,'0') FROM generate_series(1,501) value
			UNION ALL
			SELECT md5('bounded-added-'||value)::uuid,$1::uuid,'added'||lpad(value::text,3,'0') FROM generate_series(1,501) value`, server.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.pool.Exec(ctx, `
			INSERT INTO mcp_tool_rename_proposals(id,server_id,removed_tool_id,added_tool_id,removed_contract_revision_id,added_contract_revision_id,status,created_at)
			SELECT md5('bounded-proposal-'||value)::uuid,$1,
			       md5('bounded-removed-'||value)::uuid,md5('bounded-added-'||value)::uuid,
			       $2,$3,'suspected',now()-value*interval '1 minute'
			FROM generate_series(1,501) value`, server.ID, removedContractID, addedContractID); err != nil {
			t.Fatal(err)
		}

		projection, err := st.ContractGovernanceProjection(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.Renames) != 500 {
			t.Fatalf("rename projection length=%d, want 500", len(projection.Renames))
		}
		var oldestIncluded string
		if err := st.pool.QueryRow(ctx, `SELECT md5('bounded-proposal-500')::uuid::text`).Scan(&oldestIncluded); err != nil {
			t.Fatal(err)
		}
		if got := projection.Renames[499].ID; got != oldestIncluded {
			t.Fatalf("last bounded rename=%q, want %q", got, oldestIncluded)
		}
	})
}
