package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestCredentialEncryptionFailsClosedAndAuthenticates(t *testing.T) {
	if _, err := encryptAES("secret", ""); err == nil {
		t.Fatal("missing encryption key accepted")
	}
	key := strings.Repeat("12", 32)
	value, err := encryptAES("private secret", key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decryptAES(value, key)
	if err != nil || decoded != "private secret" {
		t.Fatal("encryption roundtrip failed")
	}
	if _, err = decryptAES(value, strings.Repeat("34", 32)); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestWorkerSignatureRejectsChangedOrExpiredRequest(t *testing.T) {
	secret := "runtime-only"
	timestamp := "1791334800"
	nonce := "0123456789abcdef0123456789abcdef"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("GET\n/worker/control\n" + timestamp + "\n" + nonce))
	sig := hex.EncodeToString(mac.Sum(nil))
	now := time.Unix(1791334800, 0)
	if !validWorkerSignature(secret, timestamp, nonce, sig, now) {
		t.Fatal("valid signature rejected")
	}
	if validWorkerSignature(secret, timestamp, nonce+"a", sig, now) {
		t.Fatal("modified nonce accepted")
	}
	if validWorkerSignature(secret, timestamp, nonce, sig, now.Add(time.Minute)) {
		t.Fatal("expired signature accepted")
	}
}
