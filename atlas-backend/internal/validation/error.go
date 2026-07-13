// Package validation defines reusable errors for caller-correctable input.
// Domain packages own their validation rules; this package only owns the common
// error shape used to carry a violation toward an API or another caller.
package validation

import "fmt"

// Code is a stable, machine-readable reason for a validation failure. Clients
// should branch on Code rather than comparing the human-readable Message.
type Code string

const (
	CodeRequired      Code = "required"
	CodeInvalidFormat Code = "invalid_format"
	CodeTooShort      Code = "too_short"
	CodeTooLong       Code = "too_long"
)

// Error identifies invalid caller-supplied data that the caller can correct.
// Infrastructure failures such as database, network, or hashing errors must not
// be converted into this type.
type Error struct {
	Field   string
	Code    Code
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}
