package agentclient

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	runtimeadapter "github.com/toolhub-dev/toolhub/internal/runtime"
)

type Config struct {
	ServerURL  string               `json:"serverUrl"`
	NodeID     string               `json:"nodeId"`
	AgentToken string               `json:"agentToken"`
	TaskKey    string               `json:"taskKey"`
	PrivateKey string               `json:"privateKey"`
	DataDir    string               `json:"dataDir"`
	Paths      runtimeadapter.Paths `json:"paths"`
	ConfigPath string               `json:"-"`
}

func DefaultConfigPath() (string, error) {
	if runtime.GOOS == "windows" {
		root := strings.TrimSpace(os.Getenv("PROGRAMDATA"))
		if root == "" {
			return "", errors.New("PROGRAMDATA is not set")
		}
		return filepath.Join(root, "ToolHub", "agent.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "ToolHub", "agent.json"), nil
	}
	return filepath.Join(home, ".config", "toolhub-agent", "agent.json"), nil
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return Config{}, err
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(body, &config); err != nil {
		return Config{}, err
	}
	config.ConfigPath = path
	if config.ServerURL == "" || config.NodeID == "" || config.AgentToken == "" || config.TaskKey == "" || config.DataDir == "" {
		return Config{}, errors.New("agent configuration is incomplete")
	}
	return config, nil
}

func SaveConfig(path string, config Config) error {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, encoded, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(path + ".old")
		if _, err := os.Stat(path); err == nil {
			if err := os.Rename(path, path+".old"); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(temporary, path); err != nil {
		if runtime.GOOS == "windows" {
			_ = os.Rename(path+".old", path)
		}
		return err
	}
	_ = os.Remove(path + ".old")
	return nil
}
