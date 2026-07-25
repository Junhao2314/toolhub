package policy

import "testing"

func TestResolveUsesMostSpecificMatchingPolicy(t *testing.T) {
	policies := []Policy{
		{ID: "global", ScopeType: "global", Enabled: true},
		{ID: "group", ScopeType: "node_group", ScopeID: "prod", Enabled: true},
		{ID: "source", ScopeType: "source", ScopeID: "source-1", Enabled: true},
		{ID: "skill", ScopeType: "skill", ScopeID: "skill-1", Enabled: true},
	}
	resolved, ok := Resolve(policies, Context{SkillID: "skill-1", SourceID: "source-1", NodeGroups: []string{"prod"}})
	if !ok || resolved.ID != "skill" {
		t.Fatalf("expected skill policy, got %+v ok=%v", resolved, ok)
	}
}

func TestResolveSkipsDisabledPolicy(t *testing.T) {
	resolved, ok := Resolve([]Policy{{ID: "skill", ScopeType: "skill", ScopeID: "skill-1", Enabled: false}, {ID: "global", ScopeType: "global", Enabled: true}}, Context{SkillID: "skill-1"})
	if !ok || resolved.ID != "global" {
		t.Fatalf("expected global policy, got %+v ok=%v", resolved, ok)
	}
}
