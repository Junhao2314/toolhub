package store

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Junhao2314/toolhub/internal/security"
)

func TestEnqueueJobWithOptionsDeduplicatesActiveJobsIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TOOLHUB_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TOOLHUB_TEST_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "toolhub_discovery_test") {
		t.Fatal("integration test database URL must target toolhub_discovery_test")
	}
	cipher, err := security.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := Open(ctx, databaseURL, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	externalID := "xiaping-" + uuid.NewString()
	payload := map[string]any{"kind": "xiaping", "externalId": externalID}
	options := JobOptions{MaxAttempts: 1, DeduplicateActive: true}
	first, err := st.EnqueueJobWithOptions(ctx, "skill_import", payload, false, "", options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = st.pool.Exec(ctx, "DELETE FROM jobs WHERE id=$1", first.ID) }()
	second, err := st.EnqueueJobWithOptions(ctx, "skill_import", payload, false, "", options)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("active duplicate created a second job: %s != %s", second.ID, first.ID)
	}
	var count, maxAttempts int
	if err := st.pool.QueryRow(ctx, "SELECT count(*),max(max_attempts) FROM jobs WHERE kind='skill_import' AND payload=$1::jsonb", string(first.Payload)).Scan(&count, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if count != 1 || maxAttempts != 1 {
		t.Fatalf("deduplicated jobs=%d maxAttempts=%d", count, maxAttempts)
	}
}
