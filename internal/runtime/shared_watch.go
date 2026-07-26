package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const SharedWatchDebounce = 750 * time.Millisecond

const (
	sharedWatchRetryMin = time.Second
	sharedWatchRetryMax = 30 * time.Second
)

type sharedWatchFunc func(context.Context, SharedSourceConfig, func(context.Context, []string)) error

func WatchSharedSourceUntilCancelled(ctx context.Context, source SharedSourceConfig, reconcile func(context.Context, []string)) error {
	return watchSharedSourceLoop(ctx, source, reconcile, WatchSharedSource, sharedWatchRetryMin, sharedWatchRetryMax)
}

func watchSharedSourceLoop(ctx context.Context, source SharedSourceConfig, reconcile func(context.Context, []string), watch sharedWatchFunc, minimumDelay, maximumDelay time.Duration) error {
	delay := minimumDelay
	for {
		err := watch(ctx, source, reconcile)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < maximumDelay {
			delay *= 2
			if delay > maximumDelay {
				delay = maximumDelay
			}
		}
	}
}

func WatchSharedSource(ctx context.Context, source SharedSourceConfig, reconcile func(context.Context, []string)) error {
	if source.Mode != SharedModeManaged || !source.AutoSync {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	watchPaths := []string{source.SkillsRoot, filepath.Dir(source.MCPManifest)}
	seen := map[string]bool{}
	for _, path := range watchPaths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := watcher.Add(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return err
			}
			return err
		}
	}
	reconcile(ctx, []string{"skills", "mcp"})
	pending := map[string]bool{}
	var timer *time.Timer
	var timerChannel <-chan time.Time
	resetTimer := func() {
		if timer == nil {
			timer = time.NewTimer(SharedWatchDebounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(SharedWatchDebounce)
		}
		timerChannel = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("shared source watcher stopped")
			}
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Write) == 0 {
				continue
			}
			clean := filepath.Clean(event.Name)
			if clean == filepath.Clean(source.MCPManifest) {
				pending["mcp"] = true
				resetTimer()
				continue
			}
			if filepath.Dir(clean) == filepath.Clean(source.SkillsRoot) {
				pending["skills"] = true
				resetTimer()
			}
		case <-timerChannel:
			scopes := make([]string, 0, 2)
			if pending["skills"] {
				scopes = append(scopes, "skills")
			}
			if pending["mcp"] {
				scopes = append(scopes, "mcp")
			}
			pending = map[string]bool{}
			timerChannel = nil
			if len(scopes) > 0 {
				reconcile(ctx, scopes)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return errors.New("shared source watcher stopped")
			}
			if err != nil {
				return err
			}
		}
	}
}
