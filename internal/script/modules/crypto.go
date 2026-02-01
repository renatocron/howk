package modules

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// CryptoModule provides RSA-OAEP + AES-GCM decryption for Lua scripts
type CryptoModule struct {
	keys map[string]*rsa.PrivateKey // key suffix -> private key
}

// NewCryptoModule creates a new Crypto module by loading private keys from environment variables
// It looks for env vars with prefix HOWK_LUA_CRYPTO_ and loads them as PEM-encoded RSA private keys
func NewCryptoModule() (*CryptoModule, error) {
	cm := &CryptoModule{
		keys: make(map[string]*rsa.PrivateKey),
	}

	// Load all environment variables with HOWK_LUA_CRYPTO_ prefix
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "HOWK_LUA_CRYPTO_") {
			continue
		}

		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		// Extract suffix from HOWK_LUA_CRYPTO_{SUFFIX}
		envVar := parts[0]
		keySuffix := strings.TrimPrefix(envVar, "HOWK_LUA_CRYPTO_")
		pemContent := parts[1]

		// Parse the private key
		privateKey, err := parsePrivateKey(pemContent)
		if err != nil {
			return nil, fmt.Errorf("failed to load crypto key %s: %w", keySuffix, err)
		}

		cm.keys[keySuffix] = privateKey
	}

	return cm, nil
}

// parsePrivateKey parses a PEM-encoded RSA private key (PKCS1 or PKCS8)
func parsePrivateKey(pemContent string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemContent))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	// Try PKCS1 first
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return privateKey, nil
	}

	// Try PKCS8
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key (tried PKCS1 and PKCS8): %w", err)
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not RSA private key")
	}

	return rsaKey, nil
}

// LoadCrypto loads the crypto module into the Lua state
func (cm *CryptoModule) LoadCrypto(L *lua.LState) {
	L.PreloadModule("crypto", func(L *lua.LState) int {
		mod := L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
			"decrypt_credential": cm.decryptCredential,
		})
		L.Push(mod)
		return 1
	})
}

// decryptCredential decrypts data using RSA-OAEP + AES-GCM
// Lua: decrypted_data, err = crypto.decrypt_credential(key_suffix, symmetric_key_b64, encrypted_data_b64)
func (cm *CryptoModule) decryptCredential(L *lua.LState) int {
	keySuffix := L.CheckString(1)
	symmetricKeyB64 := L.CheckString(2)
	encryptedDataB64 := L.CheckString(3)

	// Get the private key
	privateKey, ok := cm.keys[keySuffix]
	if !ok {
		L.Push(lua.LNil)
		L.Push(lua.LString(fmt.Sprintf("crypto key not found: %s", keySuffix)))
		return 2
	}

	// Decode base64
	encryptedKey, err := base64.StdEncoding.DecodeString(symmetricKeyB64)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(fmt.Sprintf("failed to decode symmetric key: %v", err)))
		return 2
	}

	encryptedData, err := base64.StdEncoding.DecodeString(encryptedDataB64)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(fmt.Sprintf("failed to decode encrypted data: %v", err)))
		return 2
	}

	// Decrypt AES key with RSA-OAEP
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, encryptedKey, nil)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(fmt.Sprintf("failed to decrypt AES key: %v", err)))
		return 2
	}

	// Decrypt data with AES-GCM
	plaintext, err := decryptAESGCM(aesKey, encryptedData)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(fmt.Sprintf("failed to decrypt data: %v", err)))
		return 2
	}

	L.Push(lua.LString(plaintext))
	return 1
}

// decryptAESGCM decrypts data using AES-256-GCM
// Format: Nonce(12) + Ciphertext + AuthTag(16)
func decryptAESGCM(key, encryptedData []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(encryptedData) < nonceSize {
		return "", fmt.Errorf("encrypted data too short")
	}

	nonce, ciphertext := encryptedData[:nonceSize], encryptedData[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt data: %w", err)
	}

	return string(plaintext), nil
}

// HasKey checks if a key with the given suffix exists
func (cm *CryptoModule) HasKey(suffix string) bool {
	_, ok := cm.keys[suffix]
	return ok
}

// KeyCount returns the number of loaded keys
func (cm *CryptoModule) KeyCount() int {
	return len(cm.keys)
}
