package security

import (
	"encoding/json"
	"regexp"
	"strings"
)

var sensitiveKey = regexp.MustCompile(`(?i)(secret|password|token|api[_-]?key|authorization|private[_-]?key|credential)`)

func RedactMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if sensitiveKey.MatchString(key) {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = redactValue(value)
	}
	return result
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return RedactMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactValue(item)
		}
		return out
	case string:
		lower := strings.ToLower(typed)
		if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "sk-") {
			return "[REDACTED]"
		}
		return typed
	default:
		return value
	}
}

func RedactJSON(data []byte) []byte {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return []byte("{\"redacted\":true}")
	}
	result, _ := json.Marshal(RedactMap(value))
	return result
}
