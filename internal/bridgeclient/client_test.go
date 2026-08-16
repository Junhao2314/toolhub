package bridgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestGovernanceClientUsesFixedTypedRoutes(t *testing.T) {
	challengeID := strings.Repeat("a", 64)
	bindingHash := strings.Repeat("b", 64)
	canaryBundle := `{"schemaVersion":1,"mode":"enforced","relayConfigurationRevisionId":"00000000-0000-0000-0000-000000000001","relayConfigurationHash":"` + strings.Repeat("a", 64) + `","globalPolicyRevisionId":"00000000-0000-0000-0000-000000000002","globalPolicyHash":"` + strings.Repeat("b", 64) + `","defaultProfileId":null,"servers":[],"profiles":[]}`
	canaryHash := strings.Repeat("c", 64)
	tests := []struct {
		name        string
		path        string
		idempotency string
		body        string
		response    string
		call        func(context.Context, *Client) error
	}{
		{name: "capability", path: "/v1/relay/governance/capability", body: `{}`, response: `{"adminProtocolVersion":1,"features":[],"routingSchemaVersions":[1],"runtime":"mcpm","runtimeVersion":"2.15.0-toolhub.1"}`, call: func(ctx context.Context, client *Client) error { _, err := client.RelayCapability(ctx); return err }},
		{name: "session canary", path: "/v1/relay/governance/session-canary", body: `{"routingBundleHash":"` + canaryHash + `","routingBundle":` + canaryBundle + `}`, response: `{"routingBundleHash":"` + canaryHash + `","profiles":[],"missingProfile":{"behavior":"empty","profileId":null,"profileRevisionId":null,"toolCount":0},"invalidProfileErrorCode":"profile_invalid","concurrentSessionCount":2,"upstreamProcesses":[]}`, call: func(ctx context.Context, client *Client) error {
			_, err := client.RelaySessionCanary(ctx, bridgeprotocol.RelaySessionCanaryRequest{RoutingBundleHash: canaryHash, RoutingBundle: json.RawMessage(canaryBundle)})
			return err
		}},
		{name: "contract observation", path: "/v1/relay/governance/contracts/observe", body: `{}`, response: `{"relayConfigurationRevisionId":"00000000-0000-0000-0000-000000000001","servers":[]}`, call: func(ctx context.Context, client *Client) error {
			_, err := client.ObserveRelayContracts(ctx)
			return err
		}},
		{name: "confirmation list", path: "/v1/relay/governance/confirmations/list", body: `{}`, response: `{"items":[]}`, call: func(ctx context.Context, client *Client) error {
			_, err := client.ListRelayConfirmations(ctx)
			return err
		}},
		{name: "confirmation approve", path: "/v1/relay/governance/confirmations/approve", idempotency: "approve-key", body: `{"challengeId":"` + challengeID + `","bindingHash":"` + bindingHash + `"}`, response: `{"challengeId":"` + challengeID + `","bindingHash":"` + bindingHash + `"}`, call: func(ctx context.Context, client *Client) error {
			_, err := client.DecideRelayConfirmation(ctx, true, "approve-key", bridgeprotocol.ConfirmationDecisionRequest{ChallengeID: challengeID, BindingHash: bindingHash})
			return err
		}},
		{name: "observation drain", path: "/v1/relay/governance/observations/drain", body: `{"afterBootId":null,"afterSequence":0,"limit":1000}`, response: `{"bootId":"boot-1","items":[],"nextSequence":0}`, call: func(ctx context.Context, client *Client) error {
			_, err := client.DrainRelayObservations(ctx, bridgeprotocol.ObservationDrainRequest{Limit: 1000})
			return err
		}},
		{name: "native client", path: "/v1/native-clients/inspect", body: `{"managedUsername":"operator","clientKind":"claude"}`, response: `{"clientKind":"claude","version":"2.1.232","supported":true}`, call: func(ctx context.Context, client *Client) error {
			_, err := client.InspectNativeClient(ctx, bridgeprotocol.NativeClientInspectionRequest{ManagedUsername: "operator", ClientKind: "claude"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{key: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return time.Unix(1, 0) }}
			client.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				if request.URL.Path != test.path || request.Header.Get(bridgeprotocol.HeaderIdempotencyKey) != test.idempotency || string(body) != test.body {
					t.Errorf("request path=%q idempotency=%q body=%s", request.URL.Path, request.Header.Get(bridgeprotocol.HeaderIdempotencyKey), body)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.response))}, nil
			})}
			if err := test.call(context.Background(), client); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientReducesUntrustedBridgeErrorToBoundedClass(t *testing.T) {
	client := &Client{key: []byte("0123456789abcdef0123456789abcdef"), now: func() time.Time { return time.Unix(1, 0) }}
	client.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"upstream_secret_code","message":"raw upstream marker","details":{"rawOutput":"secret"}}}`))}, nil
	})}
	_, err := client.RelayCapability(context.Background())
	var apiErr *bridgeprotocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != bridgeprotocol.ErrTargetUnavailable || apiErr.Message != "Target is unavailable" || !apiErr.Retryable || apiErr.Details != nil {
		t.Fatalf("Bridge client error was not bounded: %+v", apiErr)
	}
}
