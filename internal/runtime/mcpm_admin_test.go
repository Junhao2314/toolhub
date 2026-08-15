package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func TestMCPMAdminClientUsesFixedProductionSocket(t *testing.T) {
	client := NewMCPMAdminClient()
	if client.socketPath != MCPMAdminSocket {
		t.Fatalf("admin socket=%q; want %q", client.socketPath, MCPMAdminSocket)
	}
}

func TestMCPMAdminClientRejectsNonSocketAndSymlink(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	if err := os.WriteFile(regular, []byte("not a socket"), 0600); err != nil {
		t.Fatal(err)
	}
	client := newMCPMAdminClient(regular, 100*time.Millisecond)
	if _, err := client.Capability(context.Background()); err == nil {
		t.Fatal("admin client accepted a regular file")
	}

	realSocket := filepath.Join(directory, "real.sock")
	listener, err := net.Listen("unix", realSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	linkedSocket := filepath.Join(directory, "linked.sock")
	if err := os.Symlink(realSocket, linkedSocket); err != nil {
		t.Fatal(err)
	}
	client = newMCPMAdminClient(linkedSocket, 100*time.Millisecond)
	if _, err := client.Capability(context.Background()); err == nil {
		t.Fatal("admin client accepted a symlink to a socket")
	}
}

func TestMCPMAdminClientRejectsOversizedMultilineAndUnknownResponses(t *testing.T) {
	validContract := `{"ok":true,"data":{"adminProtocolVersion":1,"features":["profile-session-binding","tool-filtering","call-policy","one-shot-confirmation","payload-free-observations","routing-hot-reload"],"routingSchemaVersions":[1],"runtime":"mcpm","runtimeVersion":"2.15.0-toolhub.1"}}`
	tests := []struct {
		name     string
		response string
	}{
		{name: "oversized", response: `{"ok":true,"data":"` + strings.Repeat("x", MCPMAdminMaxMessageBytes) + `"}\n`},
		{name: "multiline", response: validContract + "\n" + validContract + "\n"},
		{name: "unknown envelope field", response: strings.TrimSuffix(validContract, "}") + `,"unexpected":true}` + "\n"},
		{name: "unknown contract field", response: strings.Replace(validContract, `"runtimeVersion":"2.15.0-toolhub.1"`, `"runtimeVersion":"2.15.0-toolhub.1","unexpected":true`, 1) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := serveAdminResponse(t, func(connection net.Conn) {
				defer connection.Close()
				_, _ = bufio.NewReader(connection).ReadBytes('\n')
				_, _ = connection.Write([]byte(test.response))
			})
			if _, err := client.Capability(context.Background()); err == nil {
				t.Fatalf("admin client accepted %s response", test.name)
			}
		})
	}
}

func TestMCPMAdminClientEnforcesDeadline(t *testing.T) {
	client := serveAdminResponse(t, func(connection net.Conn) {
		defer connection.Close()
		_, _ = bufio.NewReader(connection).ReadBytes('\n')
		time.Sleep(250 * time.Millisecond)
	})
	client.timeout = 25 * time.Millisecond
	started := time.Now()
	if _, err := client.Capability(context.Background()); err == nil {
		t.Fatal("admin client accepted a response after its deadline")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("admin deadline elapsed=%s", elapsed)
	}
}

func TestMCPMAdminConfirmationRequiresExactBindingHash(t *testing.T) {
	challengeID := strings.Repeat("a", 64)
	bindingHash := strings.Repeat("b", 64)
	client := serveAdminResponse(t, func(connection net.Conn) {
		defer connection.Close()
		line, _ := bufio.NewReader(connection).ReadBytes('\n')
		var request map[string]any
		if err := json.Unmarshal(line, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request["operation"] != "approve_confirmation" || request["challengeId"] != challengeID || request["bindingHash"] != bindingHash || len(request) != 3 {
			t.Errorf("confirmation request=%v", request)
			return
		}
		_, _ = connection.Write([]byte("{\"ok\":false,\"error\":{\"code\":\"binding_mismatch\",\"message\":\"Confirmation binding changed\"}}\n"))
	})
	_, err := client.DecideConfirmation(context.Background(), true, bridgeprotocol.ConfirmationDecisionRequest{ChallengeID: challengeID, BindingHash: bindingHash})
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "binding_mismatch" {
		t.Fatalf("confirmation error=%v", err)
	}
}

func TestMCPMAdminClientUsesTypedFixedOperations(t *testing.T) {
	relayRevisionID := "00000000-0000-0000-0000-000000000001"
	serverID := "00000000-0000-0000-0000-000000000002"
	configRevisionID := "00000000-0000-0000-0000-000000000003"
	profileID := "00000000-0000-0000-0000-000000000004"
	profileRevisionID := "00000000-0000-0000-0000-000000000005"
	toolID := "00000000-0000-0000-0000-000000000006"
	hash := strings.Repeat("a", 64)
	tests := []struct {
		name     string
		request  string
		response string
		call     func(context.Context, *MCPMAdminClient) error
	}{
		{
			name:     "reload routing",
			request:  `{"operation":"reload_routing"}`,
			response: `{"ok":true,"data":{"mode":"enforced","relayConfigurationRevisionId":"` + relayRevisionID + `","globalPolicyRevisionId":"` + configRevisionID + `","routingBundleHash":"` + hash + `","publishedProfileRevisions":[]}}` + "\n",
			call: func(ctx context.Context, client *MCPMAdminClient) error {
				status, err := client.ReloadRouting(ctx)
				if err == nil && (status.RoutingBundleHash != hash || status.RelayConfigurationRevisionID != relayRevisionID) {
					t.Fatalf("reload status=%+v", status)
				}
				return err
			},
		},
		{
			name:     "observe contracts",
			request:  `{"operation":"observe_contracts"}`,
			response: `{"ok":true,"data":{"relayConfigurationRevisionId":"` + relayRevisionID + `","servers":[{"serverId":"` + serverID + `","serverName":"acemcp","mcpConfigRevisionId":"` + configRevisionID + `","tools":[{"name":"search","runtimeName":"search","title":null,"description":"Search source","inputSchema":{"type":"object"},"outputSchema":null,"annotations":{}}]}]}}` + "\n",
			call: func(ctx context.Context, client *MCPMAdminClient) error {
				observed, err := client.ObserveContracts(ctx)
				if err == nil && (len(observed.Servers) != 1 || observed.Servers[0].Tools[0].Name != "search") {
					t.Fatalf("contract observation=%+v", observed)
				}
				return err
			},
		},
		{
			name:     "list confirmations",
			request:  `{"operation":"list_confirmations"}`,
			response: `{"ok":true,"data":{"items":[{"challengeId":"` + hash + `","bindingHash":"` + hash + `","argumentHash":"` + hash + `","createdAt":1,"expiresAt":2,"profileId":"` + profileID + `","profileRevisionId":"` + profileRevisionID + `","profileName":"coding","clientKind":"codex","serverId":"` + serverID + `","serverName":"acemcp","toolId":"` + toolID + `","toolName":"search","runtimeName":"search","mcpConfigRevisionId":"` + configRevisionID + `","contractRevisionId":"` + configRevisionID + `","globalPolicyRevisionId":"` + configRevisionID + `","decision":"confirm","reasonCodes":["mutating"],"argumentSummary":[]}]}}` + "\n",
			call: func(ctx context.Context, client *MCPMAdminClient) error {
				listed, err := client.ListConfirmations(ctx)
				if err == nil && (len(listed.Items) != 1 || listed.Items[0].BindingHash != hash) {
					t.Fatalf("confirmation list=%+v", listed)
				}
				return err
			},
		},
		{
			name:     "drain observations",
			request:  `{"operation":"drain_observations","afterBootId":"boot-1","afterSequence":7,"limit":1000}`,
			response: `{"ok":true,"data":{"bootId":"boot-1","items":[{"bootId":"boot-1","sequence":8,"observedAt":1,"minuteBucket":"2026-08-16T00:00:00Z","profileId":"` + profileID + `","profileRevisionId":"` + profileRevisionID + `","serverId":"` + serverID + `","toolId":"` + toolID + `","decision":"allow","reasonCodes":[],"outcome":"executed","durationBucket":"lt_10ms","errorClass":"none"}],"nextSequence":8}}` + "\n",
			call: func(ctx context.Context, client *MCPMAdminClient) error {
				drained, err := client.DrainObservations(ctx, bridgeprotocol.ObservationDrainRequest{AfterBootID: stringPointer("boot-1"), AfterSequence: 7, Limit: 1000})
				if err == nil && (drained.NextSequence != 8 || len(drained.Items) != 1 || drained.Items[0].Sequence != 8) {
					t.Fatalf("observation drain=%+v", drained)
				}
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := serveAdminResponse(t, func(connection net.Conn) {
				defer connection.Close()
				line, _ := bufio.NewReader(connection).ReadBytes('\n')
				if string(strings.TrimSpace(string(line))) != test.request {
					t.Errorf("admin request=%s; want %s", line, test.request)
					return
				}
				_, _ = connection.Write([]byte(test.response))
			})
			if err := test.call(context.Background(), client); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMCPMAdminDrainRejectsLimitAboveCompanionBound(t *testing.T) {
	client := newMCPMAdminClient(filepath.Join(t.TempDir(), "missing.sock"), time.Second)
	_, err := client.DrainObservations(context.Background(), bridgeprotocol.ObservationDrainRequest{Limit: 1001})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("drain limit error=%v", err)
	}
}

func stringPointer(value string) *string { return &value }

func serveAdminResponse(t *testing.T, handler func(net.Conn)) *MCPMAdminClient {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "tha-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "relay.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		handler(connection)
	}()
	t.Cleanup(func() { <-accepted })
	return newMCPMAdminClient(socketPath, time.Second)
}
