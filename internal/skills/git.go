package skills

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ImportGit(ctx context.Context, remote, subdirectory, commit string, limits Limits) (Package, string, error) {
	if err := validateGitURL(ctx, remote); err != nil {
		return Package{}, "", err
	}
	if subdirectory != "" {
		clean := filepath.Clean(filepath.FromSlash(subdirectory))
		if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Package{}, "", errors.New("invalid Git subdirectory")
		}
		subdirectory = clean
	}
	temporary, err := os.MkdirTemp("", "toolhub-git-*")
	if err != nil {
		return Package{}, "", err
	}
	defer os.RemoveAll(temporary)
	commands := [][]string{{"init", "--quiet", temporary}, {"-C", temporary, "remote", "add", "origin", remote}}
	for _, args := range commands {
		if output, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			return Package{}, "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(output)))
		}
	}
	ref := commit
	if strings.TrimSpace(ref) == "" {
		ref = "HEAD"
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", temporary, "fetch", "--quiet", "--depth=1", "--", "origin", ref).CombinedOutput(); err != nil {
		return Package{}, "", fmt.Errorf("git fetch: %s", strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", temporary, "checkout", "--quiet", "--detach", "FETCH_HEAD").CombinedOutput(); err != nil {
		return Package{}, "", fmt.Errorf("git checkout: %s", strings.TrimSpace(string(output)))
	}
	resolvedCommit, err := exec.CommandContext(ctx, "git", "-C", temporary, "rev-parse", "HEAD").Output()
	if err != nil {
		return Package{}, "", err
	}
	root := temporary
	if subdirectory != "" {
		root = filepath.Join(temporary, subdirectory)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || (resolvedRoot != temporary && !strings.HasPrefix(resolvedRoot, temporary+string(filepath.Separator))) {
		return Package{}, "", errors.New("Git subdirectory escapes repository")
	}
	pkg, err := ScanDirectory(root, limits)
	return pkg, strings.TrimSpace(string(resolvedCommit)), err
}

func validateGitURL(ctx context.Context, remote string) error {
	parsed, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "ssh") || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("Git URL must be an https or ssh URL without embedded credentials")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("local Git hosts are not allowed")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve Git host: %w", err)
	}
	for _, address := range addresses {
		if address.IP.IsLoopback() || address.IP.IsPrivate() || address.IP.IsLinkLocalUnicast() || address.IP.IsUnspecified() {
			return errors.New("Git host resolves to a private or local address")
		}
	}
	return nil
}
