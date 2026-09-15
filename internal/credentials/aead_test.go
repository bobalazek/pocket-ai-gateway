package credentials

import (
	"bytes"
	"testing"
)

func TestAEADRoundTripAndAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plaintext := []byte{'f', 'i', 'l', 'e', 0, 255}
	aad := []byte("file_1\x00key_1\x00batch\x006")
	ciphertext, nonce, err := Seal(key, plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) != len(plaintext)+16 || len(nonce) != 12 {
		t.Fatalf("unexpected sealed value: ciphertext=%d nonce=%d", len(ciphertext), len(nonce))
	}
	opened, err := Open(key, ciphertext, nonce, aad)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("opened=%q error=%v", opened, err)
	}

	for name, altered := range map[string]func() ([]byte, []byte, []byte){
		"ciphertext": func() ([]byte, []byte, []byte) {
			value := append([]byte(nil), ciphertext...)
			value[0] ^= 1
			return value, nonce, aad
		},
		"nonce": func() ([]byte, []byte, []byte) { return ciphertext, nonce[:len(nonce)-1], aad },
		"additional data": func() ([]byte, []byte, []byte) {
			return ciphertext, nonce, []byte("file_2")
		},
	} {
		t.Run(name, func(t *testing.T) {
			sealed, nonce, additionalData := altered()
			if _, err := Open(key, sealed, nonce, additionalData); err == nil {
				t.Fatal("altered value decrypted")
			}
		})
	}
}
