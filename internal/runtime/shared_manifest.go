package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Junhao2314/toolhub/internal/domain"
	"github.com/Junhao2314/toolhub/internal/protocol"
	"github.com/Junhao2314/toolhub/internal/security"
)

const sharedManifestMaxBytes = 2 << 20

type SharedManifest struct {
	Servers           []SharedMCPServer
	SourceFingerprint string
	Mode              os.FileMode
}

type SharedMCPServer struct {
	Descriptor  domain.MCPDescriptor
	Description string
	Enabled     bool
	Env         map[string]string
	Headers     map[string]string
}

type sharedManifestDocument struct {
	Version json.RawMessage                 `json:"version"`
	Comment json.RawMessage                 `json:"//"`
	Servers map[string]sharedManifestServer `json:"servers"`
}

type sharedManifestServer struct {
	Description string            `json:"description"`
	Enabled     *bool             `json:"enabled"`
	Transport   string            `json:"transport"`
	Type        string            `json:"type"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	URL         string            `json:"url"`
	Env         map[string]string `json:"env"`
	Environment map[string]string `json:"environment"`
	Headers     map[string]string `json:"headers"`
}

func ReadSharedManifest(path string, fingerprintKey []byte) (SharedManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return SharedManifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return SharedManifest{}, err
	}
	if !info.Mode().IsRegular() {
		return SharedManifest{}, errors.New("shared MCP manifest must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, sharedManifestMaxBytes+1))
	if err != nil {
		return SharedManifest{}, err
	}
	if len(body) > sharedManifestMaxBytes {
		return SharedManifest{}, errors.New("shared MCP manifest exceeds 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var document sharedManifestDocument
	if err := decoder.Decode(&document); err != nil {
		return SharedManifest{}, fmt.Errorf("parse shared MCP manifest: %w", err)
	}
	if document.Servers == nil {
		return SharedManifest{}, errors.New("shared MCP manifest requires a servers object")
	}
	names := make([]string, 0, len(document.Servers))
	for name := range document.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := SharedManifest{Servers: make([]SharedMCPServer, 0, len(names)), Mode: info.Mode().Perm()}
	for _, name := range names {
		server, err := normalizeSharedManifestServer(name, document.Servers[name], fingerprintKey)
		if err != nil {
			return SharedManifest{}, err
		}
		result.Servers = append(result.Servers, server)
	}
	redacted := make([]struct {
		Name              string `json:"name"`
		Enabled           bool   `json:"enabled"`
		Description       string `json:"description,omitempty"`
		ConfigFingerprint string `json:"configFingerprint"`
		SecretFingerprint string `json:"secretFingerprint,omitempty"`
	}, 0, len(result.Servers))
	for _, server := range result.Servers {
		redacted = append(redacted, struct {
			Name              string `json:"name"`
			Enabled           bool   `json:"enabled"`
			Description       string `json:"description,omitempty"`
			ConfigFingerprint string `json:"configFingerprint"`
			SecretFingerprint string `json:"secretFingerprint,omitempty"`
		}{server.Descriptor.Name, server.Enabled, server.Description, server.Descriptor.ConfigFingerprint, server.Descriptor.SecretFingerprint})
	}
	fingerprintBody, _ := json.Marshal(redacted)
	sum := sha256.Sum256(fingerprintBody)
	result.SourceFingerprint = hex.EncodeToString(sum[:])
	return result, nil
}

func normalizeSharedManifestServer(name string, input sharedManifestServer, fingerprintKey []byte) (SharedMCPServer, error) {
	name = strings.TrimSpace(name)
	if err := validateSharedName(name); err != nil {
		return SharedMCPServer{}, fmt.Errorf("shared MCP server name %q: %w", name, err)
	}
	enabled := false
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	transport := strings.TrimSpace(input.Transport)
	if transport == "" {
		transport = strings.TrimSpace(input.Type)
	}
	if transport == "http" {
		transport = "streamable-http"
	}
	if transport == "" {
		if strings.TrimSpace(input.Command) != "" {
			transport = "stdio"
		} else if strings.TrimSpace(input.URL) != "" {
			transport = "streamable-http"
		}
	}
	environment := input.Env
	if len(environment) == 0 {
		environment = input.Environment
	}
	headers := make(map[string]string, len(input.Headers))
	for key, value := range input.Headers {
		canonical, err := protocol.NormalizeHeaderName(key)
		if err != nil {
			return SharedMCPServer{}, fmt.Errorf("shared MCP server %q: %w", name, err)
		}
		if _, exists := headers[canonical]; exists {
			return SharedMCPServer{}, fmt.Errorf("shared MCP server %q has duplicate HTTP header names", name)
		}
		headers[canonical] = value
	}
	descriptor := domain.MCPDescriptor{
		Name:       name,
		Transport:  transport,
		Command:    input.Command,
		Args:       input.Args,
		URL:        input.URL,
		EnvKeys:    sortedStringMapKeys(environment),
		HeaderKeys: sortedStringMapKeys(headers),
	}
	var err error
	descriptor, err = protocol.NormalizeMCPDescriptor(domain.RuntimeClaude, descriptor)
	if err != nil {
		return SharedMCPServer{}, fmt.Errorf("shared MCP server %q: %w", name, err)
	}
	if len(fingerprintKey) > 0 && (len(environment) > 0 || len(headers) > 0) {
		descriptor.SecretFingerprint = security.FingerprintSecretMap(fingerprintKey, combinedSecretValues(environment, headers))
	}
	return SharedMCPServer{Descriptor: descriptor, Description: strings.TrimSpace(input.Description), Enabled: enabled, Env: cloneStringMap(environment), Headers: headers}, nil
}

func combinedSecretValues(environment, headers map[string]string) map[string]string {
	result := make(map[string]string, len(environment)+len(headers))
	for key, value := range environment {
		result["env:"+key] = value
	}
	for key, value := range headers {
		result["header:"+key] = value
	}
	return result
}

func sortedStringMapKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
