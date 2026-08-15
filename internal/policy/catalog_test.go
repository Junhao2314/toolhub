package policy

import "testing"

func TestClassifyUsesExplicitOverrideBeforeOtherSignals(t *testing.T) {
	result := Classify(ToolDescriptor{
		Name:             "delete_customer",
		ExplicitOverride: DecisionDeny,
		ReadOnlyHint:     true,
		Annotations:      map[string]any{"readOnlyHint": true},
	})
	if result.Decision != DecisionDeny {
		t.Fatalf("decision=%s want deny", result.Decision)
	}
	if len(result.Reasons) == 0 || result.Reasons[0].Code != ReasonExplicitOverride {
		t.Fatalf("reasons=%+v want explicit override first", result.Reasons)
	}
}

func TestExplicitAllowCannotLowerAnnotationRisk(t *testing.T) {
	result := Classify(ToolDescriptor{Name: "delete_item", ExplicitOverride: DecisionAllow, Annotations: map[string]any{"destructiveHint": true}})
	if result.Decision != DecisionConfirm {
		t.Fatalf("decision=%s want confirm reasons=%+v", result.Decision, result.Reasons)
	}
}

func TestReadOnlyHintCannotLowerCatalogRisk(t *testing.T) {
	result := Classify(ToolDescriptor{Name: "delete_item", ReadOnlyHint: true})
	if result.Decision != DecisionConfirm {
		t.Fatalf("decision=%s want confirm reasons=%+v", result.Decision, result.Reasons)
	}
}

func TestClassifyRaisesRiskFromAnnotationsAndCatalog(t *testing.T) {
	for _, test := range []struct {
		name string
		tool ToolDescriptor
		want string
	}{
		{"read-only", ToolDescriptor{Name: "list_files", ReadOnlyHint: true}, DecisionAllow},
		{"destructive", ToolDescriptor{Name: "delete_file"}, DecisionConfirm},
		{"credential", ToolDescriptor{Name: "rotate_api_key"}, DecisionConfirm},
		{"external publish", ToolDescriptor{Name: "publish_release"}, DecisionConfirm},
		{"annotation", ToolDescriptor{Name: "write", Annotations: map[string]any{"destructiveHint": true}}, DecisionConfirm},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Classify(test.tool)
			if got.Decision != test.want {
				t.Fatalf("decision=%s want %s reasons=%+v", got.Decision, test.want, got.Reasons)
			}
		})
	}
}

func TestClassifyUnclassifiedMutatingDefaultsToConfirm(t *testing.T) {
	result := Classify(ToolDescriptor{Name: "sync_records", Mutating: true})
	if result.Decision != DecisionConfirm {
		t.Fatalf("decision=%s want confirm", result.Decision)
	}
	if !hasReason(result, ReasonUnclassifiedMutating) {
		t.Fatalf("reasons=%+v missing unclassified reason", result.Reasons)
	}
}

func TestClassifyNameAloneCannotLowerRisk(t *testing.T) {
	result := Classify(ToolDescriptor{Name: "read_delete_preview", Description: "read-only preview"})
	if result.Decision == DecisionAllow {
		t.Fatalf("name/description lowered risk: %+v", result)
	}
}

func TestDecisionCannotLowerGlobalCeiling(t *testing.T) {
	for _, test := range []struct {
		global, profile, want string
	}{
		{DecisionAllow, DecisionAllow, DecisionAllow},
		{DecisionConfirm, DecisionAllow, DecisionConfirm},
		{DecisionDeny, DecisionConfirm, DecisionDeny},
	} {
		if got := EffectiveDecision(test.global, test.profile); got != test.want {
			t.Fatalf("effective(%s,%s)=%s want %s", test.global, test.profile, got, test.want)
		}
	}
}

func TestValidateReasonCodeIncludesRuntimeGovernanceCodes(t *testing.T) {
	for _, code := range []string{ReasonAnnotationMutating, ReasonCompatibilityMode, ReasonProfileRule} {
		if !ValidateReasonCode(code) {
			t.Fatalf("known reason code %q was rejected", code)
		}
	}
	if ValidateReasonCode("raw-upstream-marker") {
		t.Fatal("untrusted reason code was accepted")
	}
}

func hasReason(result Classification, code string) bool {
	for _, reason := range result.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
