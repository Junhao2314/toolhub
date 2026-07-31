package configmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

const LegacySchemaVersion = 11

type DatabaseIdentity struct {
	Database string
	Address  string
	Port     int
}

func (d DatabaseIdentity) Hash() string {
	sum := sha256.Sum256([]byte(d.Database + "\x00" + d.Address + "\x00" + strconv.Itoa(d.Port)))
	return hex.EncodeToString(sum[:])
}

type Snapshot struct {
	Migrations   []int64
	Identity     DatabaseIdentity
	Skills       []LegacySkill
	MCPServers   []LegacyMCPServer
	Secrets      []LegacySecret
	SkillDesired map[string][]string
	MCPDesired   map[string][]string
	UpdateCron   string
	Timezone     string
}

type LegacySkill struct {
	ID             string
	Slug           string
	Name           string
	Description    string
	SourceID       string
	SourceKind     string
	SourceName     string
	SourceURL      string
	Subdirectory   string
	SourceCommit   string
	VersionID      string
	ArtifactID     string
	ArtifactSHA256 string
	VersionSHA256  string
	SizeBytes      int64
	Archive        []byte
}

type LegacyMCPServer struct {
	ID         string
	Name       string
	Transport  string
	Command    string
	Args       []string
	URL        string
	EnvRefs    map[string]string
	HeaderRefs map[string]string
	Source     string
}

type LegacySecret struct {
	ID         string
	Kind       string
	Ciphertext []byte
}
