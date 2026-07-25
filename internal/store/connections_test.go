package store

import "testing"

func TestValidateSSHConnection(t *testing.T) {
	t.Parallel()
	validAddress := "ops@100.100.10.20"
	validKnownHosts := "100.100.10.20 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey"
	validKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nplaceholder\n-----END OPENSSH PRIVATE KEY-----"

	address, knownHosts, err := validateSSHConnection("  "+validAddress+"  ", "  "+validKnownHosts+"  ", validKey)
	if err != nil {
		t.Fatalf("validateSSHConnection() error = %v", err)
	}
	if address != validAddress || knownHosts != validKnownHosts {
		t.Fatalf("validateSSHConnection() = (%q, %q), want trimmed values", address, knownHosts)
	}
}

func TestValidateSSHConnectionRejectsUnsafeInput(t *testing.T) {
	t.Parallel()
	validKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nplaceholder\n-----END OPENSSH PRIVATE KEY-----"
	tests := []struct {
		name       string
		address    string
		knownHosts string
		privateKey string
	}{
		{name: "SSH option", address: "ops@host -o ProxyCommand=x", knownHosts: "host ssh-ed25519 AAAA", privateKey: validKey},
		{name: "multiple known hosts lines", address: "ops@host", knownHosts: "host ssh-ed25519 AAAA\nhost ssh-ed25519 BBBB", privateKey: validKey},
		{name: "carriage return", address: "ops@host", knownHosts: "host ssh-ed25519 AAAA\rmalicious", privateKey: validKey},
		{name: "invalid key", address: "ops@host", knownHosts: "host ssh-ed25519 AAAA", privateKey: "not a key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := validateSSHConnection(test.address, test.knownHosts, test.privateKey); err == nil {
				t.Fatal("validateSSHConnection() error = nil, want error")
			}
		})
	}
}
