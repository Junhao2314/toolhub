package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchSharedSourceDebouncesTopLevelSkillEvents(t *testing.T) {
	root := t.TempDir()
	source := SharedSourceConfig{
		Name:        "watch-test",
		Mode:        SharedModeManaged,
		AutoSync:    true,
		SkillsRoot:  filepath.Join(root, "skills"),
		MCPManifest: filepath.Join(root, "mcp", "servers.json"),
	}
	if err := os.MkdirAll(source.SkillsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, source.MCPManifest, `{"servers":{}}`, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan []string, 8)
	done := make(chan error, 1)
	go func() {
		done <- WatchSharedSource(ctx, source, func(_ context.Context, scopes []string) {
			calls <- append([]string(nil), scopes...)
		})
	}()
	select {
	case scopes := <-calls:
		if len(scopes) != 2 {
			t.Fatalf("startup scopes = %+v", scopes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not perform startup reconcile")
	}
	if err := os.Mkdir(filepath.Join(source.SkillsRoot, "one"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source.SkillsRoot, "two"), 0755); err != nil {
		t.Fatal(err)
	}
	select {
	case scopes := <-calls:
		if len(scopes) != 1 || scopes[0] != "skills" {
			t.Fatalf("debounced scopes = %+v", scopes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not emit debounced Skill reconcile")
	}
	select {
	case scopes := <-calls:
		t.Fatalf("top-level changes were not debounced: %+v", scopes)
	case <-time.After(SharedWatchDebounce + 250*time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after cancellation")
	}
}

func TestSharedWatcherRetriesWithoutTightLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	fake := func(context.Context, SharedSourceConfig, func(context.Context, []string)) error {
		attempt := calls.Add(1)
		if attempt < 3 {
			return errors.New("temporary watcher failure")
		}
		cancel()
		return context.Canceled
	}
	started := time.Now()
	if err := watchSharedSourceLoop(ctx, SharedSourceConfig{}, func(context.Context, []string) {}, fake, 10*time.Millisecond, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("watch attempts = %d", calls.Load())
	}
	if time.Since(started) < 25*time.Millisecond {
		t.Fatal("watch retry loop did not back off")
	}
}
