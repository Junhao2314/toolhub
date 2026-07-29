package domain

import "testing"

func TestPrincipalRoleMatching(t *testing.T) {
	operator := Principal{Roles: []string{"operator"}}
	if !operator.HasRole("admin", "operator") {
		t.Fatal("operator did not match an allowed role")
	}
	if operator.HasRole("admin") || operator.HasRole("viewer") {
		t.Fatal("operator matched a role it does not have")
	}
}

func TestHermesIsDiscoveryOnly(t *testing.T) {
	if !IsConsumerRuntime(RuntimeHermes) || !IsSkillRuntime(RuntimeHermes) || !IsMCPRuntime(RuntimeHermes) {
		t.Fatal("Hermes must remain discoverable")
	}
	if IsSkillTargetRuntime(RuntimeHermes) || IsManagedMCPRuntime(RuntimeHermes) {
		t.Fatal("Hermes was classified as a managed target")
	}
	for _, runtimeKind := range []string{RuntimeCodex, RuntimeClaude, RuntimeGrok, RuntimeOpenClaw} {
		if !IsSkillTargetRuntime(runtimeKind) {
			t.Fatalf("%s should accept imported Skill snapshots", runtimeKind)
		}
	}
	for _, runtimeKind := range []string{RuntimeCodex, RuntimeClaude} {
		if !IsManagedMCPRuntime(runtimeKind) {
			t.Fatalf("%s should accept central MCP delivery", runtimeKind)
		}
	}
}
