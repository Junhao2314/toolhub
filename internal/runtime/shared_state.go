package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/uuid"
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

func saveSharedState(dataDir string, state sharedManagedState) error {
	state.Version = 1
	state.UpdatedAt = time.Now().UTC()
	path := sharedStatePath(dataDir, state.SourceName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".new-" + uuid.NewString()
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDirectory(filepath.Dir(path))
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
