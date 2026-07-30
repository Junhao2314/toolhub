package runtime

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

type ManagedUser struct {
	Name string
	Home string
	UID  int
	GID  int
}

func LookupManagedUser(name string) (ManagedUser, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ManagedUser{}, errors.New("managed username is required")
	}
	found, err := user.Lookup(name)
	if err != nil {
		return ManagedUser{}, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrManagedUserMissing, Message: "Managed operating-system user does not exist"}
	}
	uid, err := strconv.Atoi(found.Uid)
	if err != nil {
		return ManagedUser{}, errors.New("managed user UID is invalid")
	}
	gid, err := strconv.Atoi(found.Gid)
	if err != nil {
		return ManagedUser{}, errors.New("managed user GID is invalid")
	}
	home, err := filepath.Abs(found.HomeDir)
	if err != nil || home == "" || home == string(filepath.Separator) {
		return ManagedUser{}, errors.New("managed user home is unsafe")
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil || resolvedHome != home {
		return ManagedUser{}, errors.New("managed user home must be a canonical real path")
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ManagedUser{}, errors.New("managed user home must be an existing real directory")
	}
	return ManagedUser{Name: found.Username, Home: home, UID: uid, GID: gid}, nil
}

type TargetPaths struct {
	Home         string
	SkillsRoot   string
	ClaudeConfig string
	CodexConfig  string
	MCPMRegistry string
}

func PathsFor(user ManagedUser, runtime string) (TargetPaths, error) {
	if !filepath.IsAbs(user.Home) || filepath.Clean(user.Home) == string(filepath.Separator) {
		return TargetPaths{}, errors.New("managed user home is unsafe")
	}
	paths := TargetPaths{
		Home:         user.Home,
		ClaudeConfig: filepath.Join(user.Home, ".claude.json"),
		CodexConfig:  filepath.Join(user.Home, ".codex", "config.toml"),
		MCPMRegistry: filepath.Join(user.Home, ".config", "mcpm", "servers.json"),
	}
	switch runtime {
	case bridgeprotocol.RuntimeClaude:
		paths.SkillsRoot = filepath.Join(user.Home, ".claude", "skills")
	case bridgeprotocol.RuntimeCodex:
		paths.SkillsRoot = filepath.Join(user.Home, ".codex", "skills")
	case bridgeprotocol.RuntimeHermes:
		paths.SkillsRoot = filepath.Join(user.Home, ".hermes", "skills")
	case bridgeprotocol.RuntimeSharedRelay:
	default:
		return TargetPaths{}, errors.New("unsupported target runtime")
	}
	for _, candidate := range []string{paths.SkillsRoot, paths.ClaudeConfig, paths.CodexConfig, paths.MCPMRegistry} {
		if candidate == "" {
			continue
		}
		if err := ensureWithin(user.Home, candidate); err != nil {
			return TargetPaths{}, err
		}
	}
	return paths, nil
}

func rejectSymlinkParent(target string) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) == string(filepath.Separator) {
		return errors.New("managed file path must be an absolute non-root path")
	}
	parent := filepath.Dir(filepath.Clean(target))
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(parent, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed file parent contains a symlink")
		}
		if !info.IsDir() {
			return errors.New("managed file parent is not a directory")
		}
	}
	return nil
}

func ensureWithin(root, candidate string) error {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("managed path escapes user home")
	}
	return nil
}
