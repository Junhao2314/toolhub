package domain

import "testing"

func TestWritableRuntimeBoundary(t *testing.T) {
	if !IsWritableRuntime(RuntimeClaude) || !IsWritableRuntime(RuntimeCodex) || !IsWritableRuntime(RuntimeSharedRelay) {
		t.Fatal("expected writable runtimes")
	}
	if IsWritableRuntime(RuntimeHermes) || IsWritableRuntime("grok") {
		t.Fatal("Hermes and unknown runtimes must not be writable")
	}
}
