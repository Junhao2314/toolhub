package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

var (
	bucketNonces       = []byte("nonces")
	bucketIdempotency  = []byte("idempotency")
	bucketOperations   = []byte("operations")
	bucketSaltRecovery = []byte("salt_recovery")
	bucketBackups      = []byte("backups")
)

type Journal struct {
	db *bolt.DB
}

type idempotencyRecord struct {
	RequestHash string          `json:"requestHash"`
	OperationID string          `json:"operationId,omitempty"`
	Status      int             `json:"status"`
	Response    json.RawMessage `json:"response"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type saltMemberFingerprint struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ContentHash string `json:"contentHash"`
}

type saltRecoveryRecord struct {
	OperationID       string                  `json:"operationId"`
	Function          string                  `json:"function"`
	Target            bridgeprotocol.Target   `json:"target"`
	SaltJID           string                  `json:"saltJid"`
	LocalBundle       string                  `json:"localBundle"`
	RemoteBundle      string                  `json:"remoteBundle"`
	ExpectedMembers   []saltMemberFingerprint `json:"expectedMembers"`
	CanVerifySnapshot bool                    `json:"canVerifySnapshot"`
	PreserveUnmanaged bool                    `json:"preserveUnmanaged"`
	CreatedAt         time.Time               `json:"createdAt"`
}

func OpenJournal(path string) (*Journal, error) {
	if stringsTrim(path) == "" {
		return nil, errors.New("Bridge journal path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 3 * time.Second, NoGrowSync: false})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	j := &Journal{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketNonces, bucketIdempotency, bucketOperations, bucketSaltRecovery, bucketBackups} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) Close() error { return j.db.Close() }

func (j *Journal) AcceptNonce(nonce string, now time.Time, window time.Duration) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketNonces)
		if bucket.Get([]byte(nonce)) != nil {
			return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrReplay, Message: "Bridge nonce was already used"}
		}
		cutoff := now.Add(-2 * window).Unix()
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var seenAt int64
			if json.Unmarshal(value, &seenAt) == nil && seenAt < cutoff {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		encoded, _ := json.Marshal(now.Unix())
		return bucket.Put([]byte(nonce), encoded)
	})
}

func (j *Journal) IdempotencyGet(key, requestHash string) (int, []byte, bool, error) {
	var record idempotencyRecord
	err := j.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(bucketIdempotency).Get([]byte(key))
		if value == nil {
			return bolt.ErrBucketNotFound
		}
		return json.Unmarshal(append([]byte(nil), value...), &record)
	})
	if errors.Is(err, bolt.ErrBucketNotFound) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	if record.RequestHash != requestHash {
		return 0, nil, false, &bridgeprotocol.APIError{Code: bridgeprotocol.ErrIdempotencyConflict, Message: "Idempotency key was reused with a different request"}
	}
	return record.Status, append([]byte(nil), record.Response...), true, nil
}

func (j *Journal) IdempotencyBegin(key, requestHash, operationID string) (idempotencyRecord, bool, error) {
	var record idempotencyRecord
	existing := false
	err := j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketIdempotency)
		if value := bucket.Get([]byte(key)); value != nil {
			existing = true
			if err := json.Unmarshal(append([]byte(nil), value...), &record); err != nil {
				return err
			}
			if record.RequestHash != requestHash {
				return &bridgeprotocol.APIError{Code: bridgeprotocol.ErrIdempotencyConflict, Message: "Idempotency key was reused with a different request"}
			}
			return nil
		}
		record = idempotencyRecord{RequestHash: requestHash, OperationID: operationID, CreatedAt: time.Now().UTC()}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), encoded)
	})
	return record, existing, err
}

func (j *Journal) IdempotencyPut(key, requestHash string, status int, response []byte) error {
	if len(response) > 2<<20 {
		return errors.New("idempotency response exceeds safe journal limit")
	}
	if containsSensitiveJSON(response) {
		return errors.New("refusing to persist sensitive response in Bridge journal")
	}
	record := idempotencyRecord{RequestHash: requestHash, Status: status, Response: append([]byte(nil), response...), CreatedAt: time.Now().UTC()}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketIdempotency).Put([]byte(key), encoded) })
}

func requestHash(method, path string, body []byte) string {
	sum := sha256.Sum256(append([]byte(method+"\n"+path+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

func (j *Journal) PutOperation(operation bridgeprotocol.Operation) error {
	encoded, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	if containsSensitiveJSON(encoded) {
		return errors.New("refusing to persist sensitive operation data")
	}
	return j.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketOperations).Put([]byte(operation.ID), encoded) })
}

func (j *Journal) PutRunningSaltOperation(operation bridgeprotocol.Operation, recovery saltRecoveryRecord) error {
	if operation.ID == "" || recovery.OperationID != operation.ID || recovery.SaltJID == "" || recovery.Target.ManagedHome != "" {
		return errors.New("running Salt operation recovery metadata is incomplete")
	}
	operationBody, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	recoveryBody, err := json.Marshal(recovery)
	if err != nil {
		return err
	}
	if containsSensitiveJSON(operationBody) || containsSensitiveJSON(recoveryBody) {
		return errors.New("refusing to persist sensitive Salt recovery data")
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketOperations).Put([]byte(operation.ID), operationBody); err != nil {
			return err
		}
		return tx.Bucket(bucketSaltRecovery).Put([]byte(operation.ID), recoveryBody)
	})
}

func (j *Journal) SaltRecoveries() ([]saltRecoveryRecord, error) {
	result := []saltRecoveryRecord{}
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSaltRecovery).ForEach(func(_, value []byte) error {
			var recovery saltRecoveryRecord
			if err := json.Unmarshal(value, &recovery); err != nil {
				return err
			}
			result = append(result, recovery)
			return nil
		})
	})
	return result, err
}

func (j *Journal) DeleteSaltRecovery(operationID string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSaltRecovery).Delete([]byte(operationID))
	})
}

func (j *Journal) Operation(id string) (bridgeprotocol.Operation, error) {
	var operation bridgeprotocol.Operation
	err := j.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(bucketOperations).Get([]byte(id))
		if value == nil {
			return errors.New("operation not found")
		}
		return json.Unmarshal(append([]byte(nil), value...), &operation)
	})
	return operation, err
}

func (j *Journal) RecoverOperations(now time.Time) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketOperations)
		recoveryBucket := tx.Bucket(bucketSaltRecovery)
		return bucket.ForEach(func(key, value []byte) error {
			var operation bridgeprotocol.Operation
			if err := json.Unmarshal(value, &operation); err != nil {
				return fmt.Errorf("decode Bridge operation %q: %w", key, err)
			}
			changed := false
			for index := range operation.Targets {
				if operation.Targets[index].Status == bridgeprotocol.OperationRunning {
					apiErr := &bridgeprotocol.APIError{Code: bridgeprotocol.ErrSaltJobMissing, Message: "Bridge restarted before the target result was observed", Retryable: true}
					operation.Targets[index].Status = bridgeprotocol.OperationFailed
					operation.Targets[index].Error = apiErr
					operation.Targets[index].Result = &bridgeprotocol.TargetResult{Status: bridgeprotocol.OperationFailed, Health: bridgeprotocol.HealthBlocked, Error: apiErr}
					changed = true
				}
			}
			if operation.Status == bridgeprotocol.OperationRunning {
				operation.Status = bridgeprotocol.OperationFailed
				changed = true
			}
			if !changed {
				return nil
			}
			if err := recoveryBucket.Delete(key); err != nil {
				return err
			}
			operation.UpdatedAt = now.UTC()
			encoded, err := json.Marshal(operation)
			if err != nil {
				return err
			}
			return bucket.Put(key, encoded)
		})
	})
}

// RecoverPendingIdempotency keeps only pending requests that now have a safe,
// terminal operation result. Everything else can be retried after restart.
func (j *Journal) RecoverPendingIdempotency() error {
	return j.db.Update(func(tx *bolt.Tx) error {
		idempotency := tx.Bucket(bucketIdempotency)
		operations := tx.Bucket(bucketOperations)
		cursor := idempotency.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record idempotencyRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.Status != 0 {
				continue
			}
			var operation bridgeprotocol.Operation
			operationBody := operations.Get([]byte(record.OperationID))
			if record.OperationID == "" || operationBody == nil || json.Unmarshal(operationBody, &operation) != nil || !bridgeprotocol.IsTerminalOperationStatus(operation.Status) || len(operation.Targets) != 1 || operation.Targets[0].Result == nil {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (j *Journal) PutBackup(backup bridgeprotocol.Backup) error {
	encoded, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketBackups).Put([]byte(backup.ID), encoded) })
}

func (j *Journal) Backups(targetID string) ([]bridgeprotocol.Backup, error) {
	var result []bridgeprotocol.Backup
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketBackups).ForEach(func(_, value []byte) error {
			var backup bridgeprotocol.Backup
			if err := json.Unmarshal(value, &backup); err != nil {
				return err
			}
			if targetID == "" || backup.TargetID == targetID {
				result = append(result, backup)
			}
			return nil
		})
	})
	sortBackups(result)
	return result, err
}

func (j *Journal) GCBackups(now time.Time, maxAgeDays, maxPerTarget int, remove func(bridgeprotocol.Backup) error) ([]string, error) {
	if maxAgeDays < 1 || maxAgeDays > 3650 || maxPerTarget < 1 || maxPerTarget > 1000 {
		return nil, errors.New("backup retention limits are invalid")
	}
	items, err := j.Backups("")
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	kept := map[string]int{}
	var expired []bridgeprotocol.Backup
	for _, backup := range items {
		kept[backup.TargetID]++
		if backup.CreatedAt.Before(cutoff) || kept[backup.TargetID] > maxPerTarget {
			expired = append(expired, backup)
		}
	}
	for _, backup := range expired {
		if err := remove(backup); err != nil {
			return nil, err
		}
	}
	if err := j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketBackups)
		for _, backup := range expired {
			if err := bucket.Delete([]byte(backup.ID)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	removed := make([]string, 0, len(expired))
	for _, backup := range expired {
		removed = append(removed, backup.ID)
	}
	return removed, nil
}

func containsSensitiveJSON(body []byte) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return true
	}
	return hasSensitiveValue(value)
}

func hasSensitiveValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := stringsLower(key)
			if lower == "secretvalues" || lower == "archive" || lower == "content" || lower == "manifest" || lower == "rawoutput" || lower == "plaintext" {
				return true
			}
			if hasSensitiveValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasSensitiveValue(child) {
				return true
			}
		}
	}
	return false
}

func journalSafeTargetResult(result bridgeprotocol.TargetResult) bridgeprotocol.TargetResult {
	return bridgeprotocol.TargetResult{
		Status:         result.Status,
		Health:         result.Health,
		TargetRevision: result.TargetRevision,
		BackupID:       result.BackupID,
		Repaired:       result.Repaired,
		Relay:          journalSafeRelayStatus(result.Relay),
		Error:          result.Error,
	}
}

func journalSafeRelayStatus(status *bridgeprotocol.RelayStatus) *bridgeprotocol.RelayStatus {
	if status == nil {
		return nil
	}
	safe := *status
	safe.ErrorReason = truncateJournalReason(safe.ErrorReason)
	safe.MemberStatuses = append([]bridgeprotocol.RelayMemberStatus(nil), status.MemberStatuses...)
	for index := range safe.MemberStatuses {
		safe.MemberStatuses[index].CapabilityKinds = append([]string(nil), safe.MemberStatuses[index].CapabilityKinds...)
		safe.MemberStatuses[index].ErrorReason = truncateJournalReason(safe.MemberStatuses[index].ErrorReason)
	}
	return &safe
}

func truncateJournalReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		return value[:200]
	}
	return value
}

func stringsTrim(value string) string  { return strings.TrimSpace(value) }
func stringsLower(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
