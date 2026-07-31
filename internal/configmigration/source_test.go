package configmigration

import (
	"errors"
	"testing"
)

func TestValidateMigrationLedgerRequiresExactlyOneThroughEleven(t *testing.T) {
	valid := make([]int64, LegacySchemaVersion)
	for index := range valid {
		valid[index] = int64(index + 1)
	}
	if err := validateMigrationLedger(valid); err != nil {
		t.Fatal(err)
	}
	for name, versions := range map[string][]int64{
		"missing": valid[:10],
		"extra":   append(append([]int64(nil), valid...), 12),
		"gap":     {1, 2, 3, 4, 5, 7, 8, 9, 10, 11, 12},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMigrationLedger(versions); err == nil {
				t.Fatalf("ledger %v was accepted", versions)
			}
		})
	}
}

func TestDecodeLegacyJSONRequiresExpectedShapes(t *testing.T) {
	var args []string
	if err := decodeStringArray([]byte(`["serve","--stdio"]`), &args); err != nil || len(args) != 2 {
		t.Fatalf("decode args: %#v err=%v", args, err)
	}
	var refs map[string]string
	if err := decodeStringMap([]byte(`{"TOKEN":"00000000-0000-4000-8000-000000000001"}`), &refs); err != nil || len(refs) != 1 {
		t.Fatalf("decode refs: %#v err=%v", refs, err)
	}
	if err := decodeStringMap([]byte(`[]`), &refs); err == nil {
		t.Fatal("reference array was accepted as an object")
	}
	if !errors.As(validateMigrationLedger(nil), new(*Error)) {
		t.Fatal("ledger failure did not retain a structured migration error")
	}
}
