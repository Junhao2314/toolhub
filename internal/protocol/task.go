package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type DeploySkillPayload struct {
	DeploymentID      string `json:"deploymentId"`
	DesiredGeneration int64  `json:"desiredGeneration"`
	Runtime           string `json:"runtime"`
	SkillSlug         string `json:"skillSlug"`
	VersionID         string `json:"versionId"`
	SHA256            string `json:"sha256"`
	Enabled           bool   `json:"enabled"`
}

type DeploySkillResult struct {
	ActualHash    string `json:"actualHash"`
	ActualEnabled bool   `json:"actualEnabled"`
	BackupPath    string `json:"backupPath,omitempty"`
	Changed       bool   `json:"changed"`
}

type ApplyMCPPayload struct {
	DeploymentID      string         `json:"deploymentId"`
	DesiredGeneration int64          `json:"desiredGeneration"`
	DesiredHash       string         `json:"desiredHash"`
	Runtime           string         `json:"runtime"`
	ProfileID         string         `json:"profileId"`
	ProfileName       string         `json:"profileName"`
	MCPMProfile       string         `json:"mcpmProfile"`
	Enabled           bool           `json:"enabled"`
	Servers           []MCPServerRef `json:"servers"`
}

type MCPServerRef struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Transport  string            `json:"transport"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args"`
	URL        string            `json:"url,omitempty"`
	EnvRefs    map[string]string `json:"envRefs"`
	HeaderRefs map[string]string `json:"headerRefs"`
	Overrides  json.RawMessage   `json:"overrides,omitempty"`
}

type ApplyMCPResult struct {
	ActualHash    string   `json:"actualHash"`
	ActualEnabled bool     `json:"actualEnabled"`
	Method        string   `json:"method"`
	ServerCount   int      `json:"serverCount"`
	ConfigPath    string   `json:"configPath,omitempty"`
	BackupPaths   []string `json:"backupPaths,omitempty"`
}

func TaskSigningBytes(id, kind string, payload json.RawMessage) ([]byte, error) {
	var semantic any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&semantic); err != nil {
		return nil, fmt.Errorf("decode task payload: %w", err)
	}
	canonical, err := json.Marshal(semantic)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(id)+len(kind)+len(canonical)+2)
	result = append(result, id...)
	result = append(result, '\n')
	result = append(result, kind...)
	result = append(result, '\n')
	result = append(result, canonical...)
	return result, nil
}
