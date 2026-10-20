package auth

import "testing"

func TestHashAndCheck(t *testing.T) {
	h, err := HashSecret("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckSecret(h, "correct horse") {
		t.Fatal("right secret rejected")
	}
	if CheckSecret(h, "correct horsE") {
		t.Fatal("wrong secret accepted")
	}
	h2, _ := HashSecret("correct horse")
	if h == h2 {
		t.Fatal("two hashes of one secret are identical: salt not random")
	}
	for _, bad := range []string{"", "plain", "pbkdf2-sha256$x$a$b", "md5$1$a$b"} {
		if CheckSecret(bad, "") {
			t.Fatalf("malformed hash %q matched", bad)
		}
	}
}

func TestTokens(t *testing.T) {
	tok, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if HashToken(tok) != hash || len(tok) < 40 {
		t.Fatalf("token %q hash %q", tok, hash)
	}
	tok2, _, _ := NewToken()
	if tok == tok2 {
		t.Fatal("tokens repeat")
	}
}

func TestSameKey(t *testing.T) {
	if SameKey("", "") || SameKey("x", "") {
		t.Fatal("empty configured key must match nothing")
	}
	if !SameKey("k3y", "k3y") || SameKey("k3y", "k3z") {
		t.Fatal("key compare wrong")
	}
}

func TestNormPhoneAndPIN(t *testing.T) {
	if got := NormPhone("+44 7700-900 123"); got != "+447700900123" {
		t.Fatal(got)
	}
	if NormPhone("44+1") != "441" {
		t.Fatal("a + after the start must be dropped")
	}
	for pin, ok := range map[string]bool{"1234": true, "12345678": true, "123": false, "123456789": false, "12a4": false} {
		if (ValidPIN(pin) == nil) != ok {
			t.Errorf("PIN %q", pin)
		}
	}
}

func TestGeneratePIN(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pin, err := GeneratePIN()
		if err != nil {
			t.Fatal(err)
		}
		if len(pin) != 6 || ValidPIN(pin) != nil {
			t.Fatalf("bad PIN %q", pin)
		}
		seen[pin] = true
	}
	if len(seen) < 190 {
		t.Fatalf("only %d distinct PINs in 200: not random enough", len(seen))
	}
}
