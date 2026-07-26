package protocol

import (
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func TestNormalizeMCPDescriptorCanonicalizesKeys(t *testing.T) {
	first, err := NormalizeMCPDescriptor("claude", domain.MCPDescriptor{Name: " example ", Transport: "HTTP", URL: " https://example.test/mcp ", EnvKeys: []string{"TOKEN", "REGION", "TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeMCPDescriptor("claude", domain.MCPDescriptor{Name: "example", Transport: "streamable-http", URL: "https://example.test/mcp", EnvKeys: []string{"REGION", "TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity != second.Identity || first.ConfigFingerprint != second.ConfigFingerprint {
		t.Fatalf("descriptors were not canonical: %+v %+v", first, second)
	}
}
