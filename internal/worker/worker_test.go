package worker

import (
	"testing"

	"github.com/Junhao2314/toolhub/internal/store"
)

func TestMCPSelectorsUsePluralIDs(t *testing.T) {
	deployment := store.MCPDeploymentRef{DeploymentID: "deployment-a", NodeID: "node-a", ProfileID: "profile-a", NodeGroup: "canary"}
	if !matchesMCPSelectors(deployment, makeSet([]string{"node-a"}), makeSet([]string{"profile-a"}), makeSet([]string{"deployment-a"}), "node_group", "canary") {
		t.Fatal("matching plural selectors were rejected")
	}
	if matchesMCPSelectors(deployment, nil, nil, makeSet([]string{"deployment-b"}), "", "") {
		t.Fatal("a different deployment selector was ignored")
	}
}
