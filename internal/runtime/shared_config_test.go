package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func TestDefaultSharedSourceUsesConfiguredHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "agent-home")
	paths := DefaultPaths(home)
	source := DefaultSharedSource(paths)
	if source.Mode != SharedModeObserved || source.AutoSync {
		t.Fatalf("unsafe defaults: mode=%q autoSync=%v", source.Mode, source.AutoSync)
	}
	if source.SkillsRoot != filepath.Join(home, ".shared", "skills") || source.MCPManifest != filepath.Join(home, ".shared", "mcp", "servers.json") {
		t.Fatalf("defaults did not follow home %q: %+v", home, source)
	}
	for _, kind := range []string{domain.RuntimeCodex, domain.RuntimeClaude, domain.RuntimeHermes, domain.RuntimeGrok, domain.RuntimeOpenClaw} {
		if _, ok := source.Consumers[kind]; !ok {
			t.Fatalf("missing default consumer %q", kind)
		}
	}
}

func TestAutoProbeSharedSourcesIsObservedOnly(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	if sources := AutoProbeSharedSources(paths); len(sources) != 0 {
		t.Fatalf("unexpected auto-probe without source: %+v", sources)
	}
	if err := os.MkdirAll(filepath.Join(home, ".shared", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	sources := AutoProbeSharedSources(paths)
	if len(sources) != 1 || sources[0].Mode != SharedModeObserved || sources[0].AutoSync {
		t.Fatalf("auto-probe did not remain observed-only: %+v", sources)
	}
}

func TestNormalizeSharedSourcesRejectsUnsafeTopology(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	base := DefaultSharedSource(paths)
	tests := []struct {
		name   string
		mutate func(*SharedSourceConfig)
	}{
		{name: "relative source", mutate: func(source *SharedSourceConfig) { source.SkillsRoot = "relative/skills" }},
		{name: "observed auto sync", mutate: func(source *SharedSourceConfig) { source.AutoSync = true }},
		{name: "consumer overlaps source", mutate: func(source *SharedSourceConfig) {
			consumer := source.Consumers[domain.RuntimeCodex]
			consumer.SkillsPath = filepath.Join(source.SkillsRoot, "codex")
			source.Consumers[domain.RuntimeCodex] = consumer
		}},
		{name: "consumer overwrites manifest", mutate: func(source *SharedSourceConfig) {
			consumer := source.Consumers[domain.RuntimeClaude]
			consumer.MCPPath = source.MCPManifest
			source.Consumers[domain.RuntimeClaude] = consumer
		}},
		{name: "duplicate consumer target", mutate: func(source *SharedSourceConfig) {
			claude := source.Consumers[domain.RuntimeClaude]
			hermes := source.Consumers[domain.RuntimeHermes]
			hermes.SkillsPath = claude.SkillsPath
			source.Consumers[domain.RuntimeHermes] = hermes
		}},
		{name: "inheritance without concrete parent", mutate: func(source *SharedSourceConfig) {
			claude := source.Consumers[domain.RuntimeClaude]
			claude.MCPPath = ""
			claude.MCPFormat = ""
			source.Consumers[domain.RuntimeClaude] = claude
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := cloneSharedSourceConfig(base)
			test.mutate(&source)
			if _, err := NormalizeSharedSources(paths, []SharedSourceConfig{source}); err == nil {
				t.Fatal("unsafe shared source topology was accepted")
			}
		})
	}
}

func cloneSharedSourceConfig(source SharedSourceConfig) SharedSourceConfig {
	cloned := source
	cloned.AllowedSkillRoots = append([]string(nil), source.AllowedSkillRoots...)
	cloned.Consumers = make(map[string]SharedConsumerConfig, len(source.Consumers))
	for kind, consumer := range source.Consumers {
		cloned.Consumers[kind] = consumer
	}
	return cloned
}
