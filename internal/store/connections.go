package store

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var sshAddressPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.:[\]-]+$`)

type SSHConnection struct {
	ID         string
	Address    string
	KnownHosts string
	PrivateKey []byte
}

func (s *Store) CreateSSHConnection(ctx context.Context, nodeID, address, knownHosts, privateKey, actor string) (string, error) {
	address, knownHosts, err := validateSSHConnection(address, knownHosts, privateKey)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	secretID, err := s.createSecret(ctx, tx, "ssh-key:"+nodeID+":"+uuid.NewString(), "ssh-private-key", []byte(privateKey), map[string]any{"nodeId": nodeID}, actor)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE node_connections SET enabled=false WHERE node_id=$1 AND kind='ssh' AND enabled", nodeID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO node_connections(id,node_id,kind,address,host_key_fingerprint,secret_id)
		VALUES($1,$2,'ssh',$3,$4,$5)`, id, nodeID, address, knownHosts, secretID); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func validateSSHConnection(address, knownHosts, privateKey string) (string, string, error) {
	address = strings.TrimSpace(address)
	knownHosts = strings.TrimSpace(knownHosts)
	if !sshAddressPattern.MatchString(address) || strings.ContainsAny(address, "\r\n\t ") {
		return "", "", errors.New("SSH address must use user@host with no options")
	}
	if !strings.Contains(knownHosts, " ssh-") || strings.ContainsAny(knownHosts, "\r\n") {
		return "", "", errors.New("one pinned OpenSSH known_hosts line is required")
	}
	if !strings.Contains(privateKey, "PRIVATE KEY") {
		return "", "", errors.New("an OpenSSH private key is required")
	}
	return address, knownHosts, nil
}

func (s *Store) SSHConnectionForNode(ctx context.Context, nodeID string) (SSHConnection, error) {
	var connection SSHConnection
	var secretID string
	err := s.pool.QueryRow(ctx, `SELECT c.id::text,c.address,c.host_key_fingerprint,c.secret_id::text FROM node_connections c
		WHERE c.node_id=$1 AND c.kind='ssh' AND c.enabled ORDER BY c.priority LIMIT 1`, nodeID).Scan(&connection.ID, &connection.Address, &connection.KnownHosts, &secretID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SSHConnection{}, ErrNotFound
	}
	if err != nil {
		return SSHConnection{}, err
	}
	connection.PrivateKey, err = s.SecretValue(ctx, secretID)
	return connection, err
}
