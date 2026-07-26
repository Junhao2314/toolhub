package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Junhao2314/toolhub/internal/domain"
	runtimeadapter "github.com/Junhao2314/toolhub/internal/runtime"
)

func Enroll(ctx context.Context, serverURL, token, configPath, home, dataDir string) (Config, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
		return Config{}, errors.New("server URL must use HTTPS, except loopback development URLs")
	}
	if strings.TrimSpace(token) == "" {
		return Config{}, errors.New("enrollment token is required")
	}
	if configPath == "" {
		configPath, err = DefaultConfigPath()
		if err != nil {
			return Config{}, err
		}
	}
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			return Config{}, err
		}
	}
	if dataDir == "" {
		dataDir = filepath.Join(home, ".toolhub")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Config{}, err
	}
	hostname, _ := os.Hostname()
	payload := map[string]any{
		"token": token, "hostname": hostname, "platform": runtime.GOOS, "architecture": runtime.GOARCH,
		"tailscaleIp": detectTailnetIP(), "publicKey": base64.StdEncoding.EncodeToString(publicKey),
	}
	encoded, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/agent/v1/enroll", bytes.NewReader(encoded))
	if err != nil {
		return Config{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return Config{}, fmt.Errorf("enroll agent: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Config{}, err
	}
	if response.StatusCode != http.StatusCreated {
		return Config{}, fmt.Errorf("enrollment rejected with HTTP %d", response.StatusCode)
	}
	var result domain.EnrollmentResult
	if err := json.Unmarshal(body, &result); err != nil {
		return Config{}, errors.New("invalid enrollment response")
	}
	config := Config{ServerURL: serverURL, NodeID: result.NodeID, AgentToken: result.AgentToken, TaskKey: result.TaskKey, PrivateKey: base64.StdEncoding.EncodeToString(privateKey), DataDir: dataDir, Paths: runtimeadapter.DefaultPaths(home), ConfigPath: configPath}
	if err := SaveConfig(configPath, config); err != nil {
		return Config{}, fmt.Errorf("save agent config: %w", err)
	}
	return config, nil
}

func detectTailnetIP() string {
	interfaces, _ := net.Interfaces()
	for _, networkInterface := range interfaces {
		addresses, _ := networkInterface.Addrs()
		for _, address := range addresses {
			ip, _, _ := net.ParseCIDR(address.String())
			if ip != nil && ip.To4() != nil && ip[12] == 100 && ip[13] >= 64 && ip[13] <= 127 {
				return ip.String()
			}
		}
	}
	return ""
}

func isLoopbackHost(host string) bool {
	parsed := net.ParseIP(host)
	return host == "localhost" || parsed != nil && parsed.IsLoopback()
}
