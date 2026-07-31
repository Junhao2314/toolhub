package configmigration

import "errors"

// Error keeps operational causes available to tests while exposing only a
// deliberately safe code and message to CLI callers.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

func migrationError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func PublicError(err error) (string, string) {
	var migrationErr *Error
	if errors.As(err, &migrationErr) {
		return migrationErr.Code, migrationErr.Message
	}
	return "migration_failed", "configuration migration failed"
}
