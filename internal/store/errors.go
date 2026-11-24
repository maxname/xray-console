package store

import (
	"errors"

	"modernc.org/sqlite"
)

// SQLite's extended result codes for a row that would repeat a value that has
// to be the only one of its kind.
const (
	constraintPrimaryKey = 1555
	constraintUnique     = 2067
)

// IsUnique reports whether err is the database refusing a second row with a
// value that must be unique — a name that is taken. It goes by the result
// code, not by the words of the message, which also appear in other errors
// and are not a promise.
func IsUnique(err error) bool {
	var failure *sqlite.Error
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Code() == constraintUnique || failure.Code() == constraintPrimaryKey
}
