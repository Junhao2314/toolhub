package agentclient

import (
	"testing"
	"time"
)

func TestRunnerUsesSixHourInventoryInterval(t *testing.T) {
	runner := NewRunner(Config{})
	if runner.inventoryInterval != 6*time.Hour || DefaultInventoryInterval != 6*time.Hour {
		t.Fatalf("inventory interval = %s", runner.inventoryInterval)
	}
}
