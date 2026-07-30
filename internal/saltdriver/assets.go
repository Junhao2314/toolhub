package saltdriver

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed assets/**
var saltAssets embed.FS

var assetPublishMu sync.Mutex

func (d *Driver) PublishAssets() (string, error) {
	assetPublishMu.Lock()
	defer assetPublishMu.Unlock()

	root := strings.TrimSpace(d.StateRoot)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return "", errors.New("Salt state root is unsafe")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	hasher := sha256.New()
	err := fs.WalkDir(saltAssets, "assets", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, err := saltAssets.ReadFile(path)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(path, "assets/")
		_, _ = hasher.Write([]byte(relative))
		_, _ = hasher.Write(body)
		return nil
	})
	if err != nil {
		return "", err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	versionRoot := filepath.Join(root, ".toolhub-assets", hash)
	if info, err := os.Stat(versionRoot); err == nil && info.IsDir() {
		return hash, d.activateAssets(root, versionRoot)
	}
	stage, err := os.MkdirTemp(root, ".toolhub-assets-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	err = fs.WalkDir(saltAssets, "assets", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative := strings.TrimPrefix(path, "assets/")
		if relative == "" {
			return nil
		}
		target := filepath.Join(stage, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		body, err := saltAssets.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0644)
	})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(versionRoot), 0755); err != nil {
		return "", err
	}
	if err := os.Rename(stage, versionRoot); err != nil {
		return "", err
	}
	return hash, d.activateAssets(root, versionRoot)
}

func (d *Driver) activateAssets(root, versionRoot string) error {
	for _, relative := range []string{"_modules/toolhub.py", "_states/toolhub.py", "toolhub/init.sls"} {
		source := filepath.Join(versionRoot, filepath.FromSlash(relative))
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		body, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		temporary, err := os.CreateTemp(filepath.Dir(target), ".toolhub-asset-")
		if err != nil {
			return err
		}
		name := temporary.Name()
		if _, err := temporary.Write(body); err != nil {
			temporary.Close()
			os.Remove(name)
			return err
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			os.Remove(name)
			return err
		}
		if err := temporary.Close(); err != nil {
			os.Remove(name)
			return err
		}
		if err := os.Rename(name, target); err != nil {
			os.Remove(name)
			return err
		}
	}
	return nil
}
