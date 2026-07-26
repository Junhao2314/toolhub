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

func TestSharedSelectorsUsePluralSourceAndNodeIDs(t *testing.T) {
	target := store.SharedSyncTarget{SourceID: "source-a", NodeID: "node-a", NodeGroup: "canary"}
	if !matchesSharedSelectors(target, makeSet([]string{"source-a"}), makeSet([]string{"node-a"}), "node_group", "canary") {
		t.Fatal("matching shared plural selectors were rejected")
	}
	if matchesSharedSelectors(target, makeSet([]string{"source-b"}), nil, "", "") {
		t.Fatal("a different shared source selector was ignored")
	}
	if matchesSharedSelectors(target, nil, makeSet([]string{"node-b"}), "", "") {
		t.Fatal("a different shared node selector was ignored")
	}
	if matchesSharedSelectors(target, nil, nil, "shared_source", "source-b") {
		t.Fatal("shared_source policy selector was ignored")
	}
}
