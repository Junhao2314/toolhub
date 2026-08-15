// Package policy contains the versioned, deterministic MCP call policy
// classifier used when ToolHub renders a routing bundle.
package policy

import (
	"encoding/json"
	"strings"
)

const CatalogVersion = 1

const (
	DecisionAllow   = "allow"
	DecisionConfirm = "confirm"
	DecisionDeny    = "deny"
)

const (
	ReasonExplicitOverride       = "explicit_override"
	ReasonAnnotationDeny         = "annotation_deny"
	ReasonAnnotationDestructive  = "annotation_destructive"
	ReasonAnnotationMutating     = "annotation_mutating"
	ReasonAnnotationReadOnly     = "annotation_read_only"
	ReasonCatalogDestructive     = "catalog_destructive"
	ReasonCatalogCredential      = "catalog_credential"
	ReasonCatalogExternalPublish = "catalog_external_publish"
	ReasonCatalogFinancial       = "catalog_financial"
	ReasonCatalogCommand         = "catalog_command_execution"
	ReasonSchemaMutating         = "schema_mutating"
	ReasonUnclassifiedMutating   = "unclassified_mutating"
	ReasonReviewedReadOnly       = "reviewed_read_only"
	ReasonUnclassified           = "unclassified"
	ReasonOperatorReview         = "operator_review"
	ReasonCompatibilityMode      = "compatibility-mode"
	ReasonProfileRule            = "profile-rule"
)

var validReasonCodes = map[string]struct{}{
	ReasonExplicitOverride: {}, ReasonAnnotationDeny: {}, ReasonAnnotationDestructive: {},
	ReasonAnnotationMutating: {}, ReasonAnnotationReadOnly: {}, ReasonCatalogDestructive: {},
	ReasonCatalogCredential: {}, ReasonCatalogExternalPublish: {}, ReasonCatalogFinancial: {},
	ReasonCatalogCommand: {}, ReasonSchemaMutating: {}, ReasonUnclassifiedMutating: {},
	ReasonReviewedReadOnly: {}, ReasonUnclassified: {}, ReasonOperatorReview: {},
	ReasonCompatibilityMode: {}, ReasonProfileRule: {},
	"schema_changed": {}, "presentation_changed": {}, "server_visibility": {}, "rename_confirmed": {},
}

// ToolDescriptor is the payload-free subset of an observed tool used for
// classification. Schema and annotations are retained only for governance;
// call arguments/results never enter this package.
type ToolDescriptor struct {
	Name             string
	Description      string
	InputSchema      json.RawMessage
	OutputSchema     json.RawMessage
	Annotations      map[string]any
	ExplicitOverride string
	ReadOnlyHint     bool
	Mutating         bool
}

type Reason struct {
	Code   string
	Detail string
}

type Classification struct {
	CatalogVersion int
	Decision       string
	Reasons        []Reason
}

// Classify applies governance inputs in their documented order. Pattern
// matches can raise a decision, but cannot turn an unknown tool into allow.
func Classify(tool ToolDescriptor) Classification {
	result := Classification{CatalogVersion: CatalogVersion, Decision: DecisionAllow, Reasons: []Reason{}}
	if validDecision(tool.ExplicitOverride) {
		result.Decision = tool.ExplicitOverride
		result.Reasons = append(result.Reasons, Reason{Code: ReasonExplicitOverride})
		if tool.ExplicitOverride == DecisionDeny {
			return result
		}
	}

	annotations := tool.Annotations
	if annotationBool(annotations, "denyHint") || annotationString(annotations, "decision") == DecisionDeny {
		result.Decision = DecisionDeny
		result.Reasons = append(result.Reasons, Reason{Code: ReasonAnnotationDeny})
		return result
	}
	if annotationBool(annotations, "destructiveHint") || annotationBool(annotations, "destructive") {
		raise(&result, DecisionConfirm, ReasonAnnotationDestructive)
	}
	if annotationBool(annotations, "mutatingHint") || annotationBool(annotations, "mutating") {
		raise(&result, DecisionConfirm, ReasonAnnotationMutating)
	}
	readOnly := tool.ReadOnlyHint || annotationBool(annotations, "readOnlyHint") || annotationBool(annotations, "readOnly") || schemaIndicatesReadOnly(tool.InputSchema) || schemaIndicatesReadOnly(tool.OutputSchema)
	if readOnly && !tool.Mutating {
		result.Reasons = append(result.Reasons, Reason{Code: ReasonAnnotationReadOnly})
	}

	name := strings.ToLower(strings.TrimSpace(tool.Name + " " + tool.Description))
	for _, marker := range []struct {
		patterns []string
		code     string
	}{
		{[]string{"delete", "destroy", "drop", "purge", "revoke", "terminate"}, ReasonCatalogDestructive},
		{[]string{"credential", "password", "secret", "token", "api_key", "apikey", "rotate_key"}, ReasonCatalogCredential},
		{[]string{"publish", "release", "deploy", "send_external", "post_public"}, ReasonCatalogExternalPublish},
		{[]string{"payment", "charge", "refund", "invoice", "transfer"}, ReasonCatalogFinancial},
		{[]string{"shell", "exec", "execute_command", "run_command", "sudo"}, ReasonCatalogCommand},
	} {
		if containsAny(name, marker.patterns) {
			raise(&result, DecisionConfirm, marker.code)
		}
	}
	if schemaSuggestsMutation(tool.InputSchema) || schemaSuggestsMutation(tool.OutputSchema) {
		raise(&result, DecisionConfirm, ReasonSchemaMutating)
	}
	if result.Decision == DecisionConfirm {
		return result
	}
	// A mutating hint without a named catalog marker is deliberately
	// confirmation-gated. An unknown tool is also not silently allowed.
	if tool.Mutating {
		raise(&result, DecisionConfirm, ReasonUnclassifiedMutating)
	} else if !readOnly {
		raise(&result, DecisionConfirm, ReasonUnclassified)
	} else {
		result.Reasons = append(result.Reasons, Reason{Code: ReasonReviewedReadOnly})
	}
	return result
}

func EffectiveDecision(global, profile string) string {
	if !validDecision(global) {
		global = DecisionConfirm
	}
	if !validDecision(profile) {
		profile = DecisionConfirm
	}
	if decisionRank(profile) > decisionRank(global) {
		return profile
	}
	return global
}

func ValidateDecision(value string) bool { return validDecision(value) }

func ValidateReasonCode(value string) bool {
	_, ok := validReasonCodes[value]
	return ok
}

func raise(result *Classification, decision, reason string) {
	if decisionRank(decision) > decisionRank(result.Decision) {
		result.Decision = decision
	}
	result.Reasons = append(result.Reasons, Reason{Code: reason})
}

func validDecision(value string) bool {
	return value == DecisionAllow || value == DecisionConfirm || value == DecisionDeny
}

func decisionRank(value string) int {
	switch value {
	case DecisionDeny:
		return 2
	case DecisionConfirm:
		return 1
	default:
		return 0
	}
}

func annotationBool(values map[string]any, key string) bool {
	value, ok := values[key]
	if !ok {
		return false
	}
	parsed, ok := value.(bool)
	return ok && parsed
}

func annotationString(values map[string]any, key string) string {
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func containsAny(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}

func schemaSuggestsMutation(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return schemaValueSuggestsMutation(value)
}

func schemaIndicatesReadOnly(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return schemaValueIndicatesReadOnly(value)
}

func schemaValueIndicatesReadOnly(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			lower := strings.ToLower(key)
			if lower == "readonly" || lower == "read_only" || lower == "x-toolhub-read-only" {
				if marker, ok := child.(bool); ok && marker {
					return true
				}
			}
			if schemaValueIndicatesReadOnly(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if schemaValueIndicatesReadOnly(child) {
				return true
			}
		}
	}
	return false
}

func schemaValueSuggestsMutation(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			lower := strings.ToLower(key)
			if lower == "readOnly" || lower == "readonly" {
				if readOnly, ok := child.(bool); ok && readOnly {
					continue
				}
			}
			if lower == "writeOnly" || lower == "writeonly" || lower == "mutating" || lower == "destructive" {
				if marker, ok := child.(bool); ok && marker {
					return true
				}
			}
			if schemaValueSuggestsMutation(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if schemaValueSuggestsMutation(child) {
				return true
			}
		}
	}
	return false
}
