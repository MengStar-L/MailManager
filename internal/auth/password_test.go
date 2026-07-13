package auth

import "testing"

func TestPasswordHasherRoundTrip(t *testing.T) {
	hasher, err := NewPasswordHasher(testPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := hasher.Verify(encoded, "correct horse battery staple")
	if err != nil || !valid {
		t.Fatalf("valid password: valid=%v err=%v", valid, err)
	}
	valid, err = hasher.Verify(encoded, "wrong password")
	if err != nil || valid {
		t.Fatalf("wrong password: valid=%v err=%v", valid, err)
	}
}

func TestPasswordHasherRejectsUntrustedParameters(t *testing.T) {
	encoded := "$argon2id$v=19$m=4294967295,t=3,p=2$c2FsdHNhbHQ$YWJjZGVmZ2hpamtsbW5vcA"
	if _, err := VerifyPassword(encoded, "password"); err == nil {
		t.Fatal("expected excessive memory parameter to be rejected before hashing")
	}
}

func testPasswordParams() PasswordParams {
	return PasswordParams{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}
}
