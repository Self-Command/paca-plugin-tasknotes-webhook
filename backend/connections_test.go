package main

import (
	"strings"
	"testing"
)

func TestConnectionSecretValidation(t *testing.T) {
	for _, secret := range []string{"", strings.Repeat("a", 64), strings.Repeat("b", 32), strings.Repeat("c", 256)} {
		c := connectionInput{Name: "Official desktop", Secret: secret}
		if !validateConnection(&c) {
			t.Fatal("valid sender Secret rejected")
		}
	}
	for _, secret := range []string{strings.Repeat("a", 31), strings.Repeat("b", 257), " " + strings.Repeat("c", 64), strings.Repeat("d", 32) + "\n", strings.Repeat("e", 32) + "\x00"} {
		c := connectionInput{Name: "Official desktop", Secret: secret}
		if validateConnection(&c) {
			t.Fatal("invalid sender Secret accepted")
		}
	}
}
