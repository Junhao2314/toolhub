package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapUserWriteError(t *testing.T) {
	tests := []struct {
		constraint string
		want       error
	}{
		{constraint: "users_username_lower_idx", want: ErrUsernameUnavailable},
		{constraint: "users_email_lower_idx", want: ErrEmailUnavailable},
	}
	for _, test := range tests {
		err := mapUserWriteError(&pgconn.PgError{Code: "23505", ConstraintName: test.constraint})
		if !errors.Is(err, test.want) {
			t.Fatalf("constraint %q mapped to %v, want %v", test.constraint, err, test.want)
		}
	}
}
