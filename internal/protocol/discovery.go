package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/textproto"
	"sort"
	"strings"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func NormalizeMCPDescriptor(runtimeKind string, descriptor domain.MCPDescriptor) (domain.MCPDescriptor, error) {
	descriptor.Name = strings.TrimSpace(descriptor.Name)
	descriptor.Command = strings.TrimSpace(descriptor.Command)
	descriptor.URL = strings.TrimSpace(descriptor.URL)
	descriptor.Transport = strings.ToLower(strings.TrimSpace(descriptor.Transport))
	if descriptor.Transport == "http" {
		descriptor.Transport = "streamable-http"
	}
	if !domain.IsMCPRuntime(runtimeKind) {
		return domain.MCPDescriptor{}, errors.New("invalid MCP runtime")
	}
	if descriptor.Name == "" || (descriptor.Transport != "stdio" && descriptor.Transport != "sse" && descriptor.Transport != "streamable-http") {
		return domain.MCPDescriptor{}, errors.New("valid MCP name and transport are required")
	}
	if descriptor.Transport == "stdio" && descriptor.Command == "" {
		return domain.MCPDescriptor{}, errors.New("stdio MCP descriptor requires a command")
	}
	if descriptor.Transport != "stdio" && descriptor.URL == "" {
		return domain.MCPDescriptor{}, errors.New("network MCP descriptor requires a URL")
	}
	if descriptor.Args == nil {
		descriptor.Args = []string{}
	}
	descriptor.EnvKeys = uniqueSortedStrings(descriptor.EnvKeys)
	headerKeys, err := normalizeHeaderKeys(descriptor.HeaderKeys)
	if err != nil {
		return domain.MCPDescriptor{}, err
	}
	descriptor.HeaderKeys = headerKeys
	descriptor.Identity = MCPIdentity(runtimeKind, descriptor.Name)
	descriptor.ConfigFingerprint = MCPConfigFingerprint(descriptor)
	return descriptor, nil
}

func NormalizeHeaderName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !validHeaderName(value) {
		return "", errors.New("invalid MCP HTTP header name")
	}
	return textproto.CanonicalMIMEHeaderKey(value), nil
}

func normalizeHeaderKeys(values []string) ([]string, error) {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		canonical, err := NormalizeHeaderName(value)
		if err != nil {
			return nil, err
		}
		set[canonical] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func MCPIdentity(runtimeKind, name string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(runtimeKind) + "\n" + strings.TrimSpace(name)))
	return hex.EncodeToString(sum[:])
}

func MCPConfigFingerprint(descriptor domain.MCPDescriptor) string {
	canonical := struct {
		Transport  string   `json:"transport"`
		Command    string   `json:"command"`
		Args       []string `json:"args"`
		URL        string   `json:"url"`
		EnvKeys    []string `json:"envKeys"`
		HeaderKeys []string `json:"headerKeys"`
	}{descriptor.Transport, descriptor.Command, descriptor.Args, descriptor.URL, uniqueSortedStrings(descriptor.EnvKeys), uniqueSortedStrings(descriptor.HeaderKeys)}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
