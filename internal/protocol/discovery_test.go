package protocol

import (
	"testing"

	"github.com/Junhao2314/toolhub/internal/domain"
)

func TestNormalizeMCPDescriptorCanonicalizesKeys(t *testing.T) {
	first, err := NormalizeMCPDescriptor("claude", domain.MCPDescriptor{Name: " example ", Transport: "HTTP", URL: " https://example.test/mcp ", EnvKeys: []string{"TOKEN", "REGION", "TOKEN"}, HeaderKeys: []string{"authorization", "X-API-Key", "Authorization"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeMCPDescriptor("claude", domain.MCPDescriptor{Name: "example", Transport: "streamable-http", URL: "https://example.test/mcp", EnvKeys: []string{"REGION", "TOKEN"}, HeaderKeys: []string{"Authorization", "X-Api-Key"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity != second.Identity || first.ConfigFingerprint != second.ConfigFingerprint {
		t.Fatalf("descriptors were not canonical: %+v %+v", first, second)
	}
	if len(first.HeaderKeys) != 2 || first.HeaderKeys[0] != "Authorization" || first.HeaderKeys[1] != "X-Api-Key" {
		t.Fatalf("header keys were not canonicalized: %+v", first.HeaderKeys)
	}
}

func TestNormalizeMCPDescriptorRejectsInvalidHeaderName(t *testing.T) {
	_, err := NormalizeMCPDescriptor("claude", domain.MCPDescriptor{Name: "example", Transport: "streamable-http", URL: "https://example.test/mcp", HeaderKeys: []string{"Bad Header"}})
	if err == nil {
		t.Fatal("invalid HTTP header name was accepted")
	}
}
