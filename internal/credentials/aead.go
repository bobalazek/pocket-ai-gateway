package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// Seal encrypts plaintext with AES-GCM and authenticates additionalData.
func Seal(key, plaintext, additionalData []byte) ([]byte, []byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, plaintext, additionalData), nonce, nil
}

// Open authenticates and decrypts ciphertext produced by Seal.
func Open(key, ciphertext, nonce, additionalData []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, errors.New("encrypted value cannot be decrypted")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, errors.New("encrypted value cannot be decrypted")
	}
	return plaintext, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
