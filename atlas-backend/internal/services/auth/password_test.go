package authservice

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := hashPassword("a correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if strings.Contains(encoded, "correct horse") {
		t.Fatal("encoded hash contains plaintext password")
	}

	valid, err := verifyPassword("a correct horse battery staple", encoded)
	if err != nil || !valid {
		t.Fatalf("verify correct password = %v, %v", valid, err)
	}
	valid, err = verifyPassword("a different password", encoded)
	if err != nil || valid {
		t.Fatalf("verify wrong password = %v, %v", valid, err)
	}
}

func TestPasswordHashesUseUniqueSalts(t *testing.T) {
	first, err := hashPassword("the same long password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword("the same long password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two password hashes are identical; salts should be unique")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if _, err := verifyPassword("password", "not-an-argon-hash"); err == nil {
		t.Fatal("verifyPassword() error = nil, want malformed-hash error")
	}
}
