//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKeyRequiresRootOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hmac.key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 32)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(path); err == nil {
		t.Fatal("expected world-readable key rejection")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := loadKey(path); err == nil {
			t.Fatal("expected non-root-owned key rejection")
		}
		return
	}
	key, err := loadKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
}
