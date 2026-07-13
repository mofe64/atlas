package validation

import "testing"

func TestErrorIncludesFieldAndMessage(t *testing.T) {
	err := &Error{Field: "email", Code: CodeInvalidFormat, Message: "must be a valid email address"}

	if got, want := err.Error(), "email: must be a valid email address"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if err.Code != CodeInvalidFormat {
		t.Fatalf("Code = %q, want %q", err.Code, CodeInvalidFormat)
	}
}
