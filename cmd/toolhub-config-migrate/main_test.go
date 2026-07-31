package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunReturnsStructuredSafeOptionError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"unexpected-positional-value"}, &stdout, &stderr)
	if exit != 2 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q", exit, stdout.String())
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "invalid_options" || strings.Contains(envelope.Error.Message, "unexpected-positional-value") || strings.Contains(envelope.Error.Message, "TOOLHUB_") {
		t.Fatalf("unsafe error envelope: %+v", envelope)
	}
}
