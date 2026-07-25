package policy

import "sort"

type Policy struct {
	ID        string
	ScopeType string
	ScopeID   string
	Enabled   bool
	Settings  map[string]any
}

type Context struct {
	SkillID    string
	SourceID   string
	NodeGroups []string
}

func Resolve(policies []Policy, context Context) (Policy, bool) {
	candidates := make([]Policy, 0, len(policies))
	for _, policy := range policies {
		if !policy.Enabled || !matches(policy, context) {
			continue
		}
		candidates = append(candidates, policy)
	}
	if len(candidates) == 0 {
		return Policy{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool { return rank(candidates[i].ScopeType) > rank(candidates[j].ScopeType) })
	return candidates[0], true
}

func matches(policy Policy, context Context) bool {
	switch policy.ScopeType {
	case "skill":
		return policy.ScopeID == context.SkillID
	case "source":
		return policy.ScopeID == context.SourceID
	case "node_group":
		for _, group := range context.NodeGroups {
			if policy.ScopeID == group {
				return true
			}
		}
		return false
	case "global":
		return policy.ScopeID == ""
	default:
		return false
	}
}

func rank(scope string) int {
	switch scope {
	case "skill":
		return 4
	case "source":
		return 3
	case "node_group":
		return 2
	case "global":
		return 1
	default:
		return 0
	}
}
