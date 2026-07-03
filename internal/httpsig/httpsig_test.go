package httpsig

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSignVerifyPost(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)

	r := httptest.NewRequest("POST", "https://remote.example/inbox?x=1", nil)
	r.Header.Set("Content-Type", "application/activity+json")
	if err := Sign(r, body, key, "https://us.example/actor#main-key"); err != nil {
		t.Fatal(err)
	}

	sig, err := ParseSignature(r.Header.Get("Signature"))
	if err != nil {
		t.Fatal(err)
	}
	if sig.KeyID != "https://us.example/actor#main-key" {
		t.Errorf("keyId = %q", sig.KeyID)
	}
	if err := Verify(r, body, &key.PublicKey, sig); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestSignVerifyGet(t *testing.T) {
	key := testKey(t)
	r := httptest.NewRequest("GET", "https://remote.example/actor", nil)
	r.Header.Set("Accept", "application/activity+json")
	if err := Sign(r, nil, key, "https://us.example/actor#main-key"); err != nil {
		t.Fatal(err)
	}
	sig, err := ParseSignature(r.Header.Get("Signature"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(r, nil, &key.PublicKey, sig); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestTamperedBodyRejected(t *testing.T) {
	key := testKey(t)
	body := []byte(`{"type":"Follow"}`)
	r := httptest.NewRequest("POST", "https://remote.example/inbox", nil)
	r.Header.Set("Content-Type", "application/activity+json")
	if err := Sign(r, body, key, "k"); err != nil {
		t.Fatal(err)
	}
	sig, _ := ParseSignature(r.Header.Get("Signature"))
	err := Verify(r, []byte(`{"type":"Delete"}`), &key.PublicKey, sig)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("want digest error, got %v", err)
	}
}

func TestStaleDateRejected(t *testing.T) {
	key := testKey(t)
	body := []byte(`{}`)
	r := httptest.NewRequest("POST", "https://remote.example/inbox", nil)
	r.Header.Set("Content-Type", "application/activity+json")
	r.Header.Set("Date", time.Now().Add(-2*time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	if err := Sign(r, body, key, "k"); err != nil {
		t.Fatal(err)
	}
	sig, _ := ParseSignature(r.Header.Get("Signature"))
	err := Verify(r, body, &key.PublicKey, sig)
	if err == nil || !strings.Contains(err.Error(), "skew") {
		t.Errorf("want clock-skew error, got %v", err)
	}
}

func TestWrongKeyRejected(t *testing.T) {
	key, other := testKey(t), testKey(t)
	body := []byte(`{}`)
	r := httptest.NewRequest("POST", "https://remote.example/inbox", nil)
	r.Header.Set("Content-Type", "application/activity+json")
	if err := Sign(r, body, key, "k"); err != nil {
		t.Fatal(err)
	}
	sig, _ := ParseSignature(r.Header.Get("Signature"))
	if err := Verify(r, body, &other.PublicKey, sig); err == nil {
		t.Error("verify with wrong key succeeded")
	}
}

func TestUnsignedDigestRejected(t *testing.T) {
	// A POST whose signature does not cover the digest header must fail
	// even if the signature itself is valid.
	key := testKey(t)
	r := httptest.NewRequest("POST", "https://remote.example/inbox", nil)
	r.Header.Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	msg, err := signingString(r, []string{"(request-target)", "host", "date"})
	if err != nil {
		t.Fatal(err)
	}
	_ = msg
	sig := &Signature{KeyID: "k", Headers: []string{"(request-target)", "host", "date"}}
	err = Verify(r, []byte(`{}`), &key.PublicKey, sig)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("want missing-digest error, got %v", err)
	}
}
