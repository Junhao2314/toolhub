package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type sharedManagedState struct {
	Version           int                               `json:"version"`
	SourceName        string                            `json:"sourceName"`
	ConfigFingerprint string                            `json:"configFingerprint"`
	Links             map[string]map[string]managedLink `json:"links"`
	MCP               map[string]managedMCPState        `json:"mcp"`
	UpdatedAt         time.Time                         `json:"updatedAt"`
}

type managedLink struct {
	TargetPath     string `json:"targetPath"`
	ExpectedTarget string `json:"expectedTarget"`
}

type managedMCPState struct {
	Path                string            `json:"path"`
	Format              string            `json:"format"`
	Entries             map[string]string `json:"entries"`
	DocumentFingerprint string            `json:"documentFingerprint"`
}

var safeStateName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func loadSharedState(dataDir, sourceName string) (sharedManagedState, error) {
	state := newSharedState(sourceName)
	body, err := os.ReadFile(sharedStatePath(dataDir, sourceName))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return sharedManagedState{}, err
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return sharedManagedState{}, errors.New("shared source ownership state is invalid")
	}
	if state.Version != 1 || state.SourceName != sourceName {
		return sharedManagedState{}, errors.New("shared source ownership state identity is invalid")
	}
	if state.Links == nil {
		state.Links = map[string]map[string]managedLink{}
	}
	if state.MCP == nil {
		state.MCP = map[string]managedMCPState{}
	}
	return state, nil
}

func newSharedState(sourceName string) sharedManagedState {
	return sharedManagedState{Version: 1, SourceName: sourceName, Links: map[string]map[string]managedLink{}, MCP: map[string]managedMCPState{}}
}

func sharedStatePath(dataDir, sourceName string) string {
	name := safeStateName.ReplaceAllString(sourceName, "-")
	return filepath.Join(dataDir, "shared", name+".json")
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		// Some platforms and filesystems do not support directory fsync.
		return nil
	}
	return nil
}
