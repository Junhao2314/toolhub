package configmigration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type MCPNormalization struct {
	LegacyID            string `json:"legacyId"`
	OriginalName        string `json:"originalName"`
	NormalizedName      string `json:"normalizedName"`
	OriginalTransport   string `json:"originalTransport"`
	NormalizedTransport string `json:"normalizedTransport"`
}

type ProfileMembershipReport struct {
	Name       string `json:"name"`
	Skills     int    `json:"skills"`
	MCPServers int    `json:"mcpServers"`
}

type Report struct {
	SchemaVersion      int                       `json:"schemaVersion"`
	Status             string                    `json:"status"`
	AlreadyImported    bool                      `json:"alreadyImported"`
	SourceFingerprint  string                    `json:"sourceFingerprint"`
	LegacyMigrations   []int64                   `json:"legacyMigrations"`
	Skills             int                       `json:"skills"`
	MCPServers         int                       `json:"mcpServers"`
	MCPSecrets         int                       `json:"mcpSecrets"`
	RelayMCPServers    int                       `json:"relayMcpServers"`
	Profiles           int                       `json:"profiles"`
	Renames            int                       `json:"renames"`
	TransportChanges   int                       `json:"transportChanges"`
	MCPNormalizations  []MCPNormalization        `json:"mcpNormalizations"`
	ProfileMemberships []ProfileMembershipReport `json:"profileMemberships"`
	UpdateCron         string                    `json:"updateCron"`
	Timezone           string                    `json:"timezone"`
}

func (r Report) Human() []byte {
	var output bytes.Buffer
	fmt.Fprintln(&output, "ToolHub configuration import")
	fmt.Fprintf(&output, "status: %s\n", r.Status)
	fmt.Fprintf(&output, "source fingerprint: %s\n", r.SourceFingerprint)
	fmt.Fprintf(&output, "source schema versions: 1..%d\n", len(r.LegacyMigrations))
	fmt.Fprintf(&output, "Library Skills: %d\n", r.Skills)
	fmt.Fprintf(&output, "MCP definitions: %d\n", r.MCPServers)
	fmt.Fprintf(&output, "MCP Secret records: %d (values redacted)\n", r.MCPSecrets)
	fmt.Fprintf(&output, "enabled shared-relay MCP definitions: %d\n", r.RelayMCPServers)
	fmt.Fprintf(&output, "generated Profiles: %d\n", r.Profiles)
	fmt.Fprintf(&output, "update schedule: %s [%s]\n", r.UpdateCron, r.Timezone)
	for _, profile := range r.ProfileMemberships {
		fmt.Fprintf(&output, "Profile %s: Skills=%d MCP=%d\n", profile.Name, profile.Skills, profile.MCPServers)
	}
	normalizations := append([]MCPNormalization(nil), r.MCPNormalizations...)
	sort.Slice(normalizations, func(i, j int) bool { return normalizations[i].LegacyID < normalizations[j].LegacyID })
	for _, item := range normalizations {
		if item.OriginalName == item.NormalizedName && item.OriginalTransport == item.NormalizedTransport {
			continue
		}
		fmt.Fprintf(&output, "MCP %s: %s/%s -> %s/%s\n", item.LegacyID, item.OriginalName, item.OriginalTransport, item.NormalizedName, item.NormalizedTransport)
	}
	return output.Bytes()
}

func (r Report) WriteJSON(path string) error {
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return migrationError("report_write_failed", "redacted JSON report could not be created", err)
	}
	createdInfo, err := file.Stat()
	if err != nil || !createdInfo.Mode().IsRegular() {
		_ = file.Close()
		return migrationError("report_write_failed", "redacted JSON report permissions are invalid", err)
	}
	removeCreated := func() {
		if currentInfo, err := os.Lstat(path); err == nil && os.SameFile(createdInfo, currentInfo) {
			_ = os.Remove(path)
		}
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		removeCreated()
		return migrationError("report_write_failed", "redacted JSON report permissions are invalid", err)
	}
	createdInfo, err = file.Stat()
	if err != nil || createdInfo.Mode().Perm() != 0o600 {
		_ = file.Close()
		removeCreated()
		return migrationError("report_write_failed", "redacted JSON report permissions are invalid", err)
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			removeCreated()
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(r); err != nil {
		return migrationError("report_write_failed", "redacted JSON report could not be encoded", err)
	}
	if err := file.Sync(); err != nil {
		return migrationError("report_write_failed", "redacted JSON report could not be synchronized", err)
	}
	if err := file.Close(); err != nil {
		return migrationError("report_write_failed", "redacted JSON report could not be finalized", err)
	}
	success = true
	return nil
}
