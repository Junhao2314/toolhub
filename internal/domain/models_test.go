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
