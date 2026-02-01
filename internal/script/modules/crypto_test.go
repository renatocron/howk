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
	"testing"
)

// generateTestRSAKeyPair generates a test RSA key pair and returns them as PEM strings
func generateTestRSAKeyPair(t *testing.T) (privateKeyPEM string, publicKeyPEM string) {
	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	// Encode private key to PKCS1 PEM
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	}
	privateKeyPEM = string(pem.EncodeToMemory(privateKeyBlock))

	// Encode public key to PKIX PEM
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal public key: %v", err)
	}
	publicKeyBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	}
	publicKeyPEM = string(pem.EncodeToMemory(publicKeyBlock))

	return privateKeyPEM, publicKeyPEM
}

// encryptWithOAEP encrypts data using RSA-OAEP + AES-GCM (compatible with Node.js code)
func encryptWithOAEP(t *testing.T, publicKey *rsa.PublicKey, plaintext string) (encryptedKeyB64, encryptedDataB64 string) {
	// Generate AES-256 key
	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		t.Fatalf("Failed to generate AES key: %v", err)
	}

	// Encrypt data with AES-GCM
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("Failed to create AES cipher: %v", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("Failed to create GCM: %v", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("Failed to generate nonce: %v", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encrypt AES key with RSA-OAEP
	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, aesKey, nil)
	if err != nil {
		t.Fatalf("Failed to encrypt AES key: %v", err)
	}

	return base64.StdEncoding.EncodeToString(encryptedKey),
		base64.StdEncoding.EncodeToString(ciphertext)
}

func TestParsePrivateKey_PKCS1(t *testing.T) {
	privateKeyPEM, _ := generateTestRSAKeyPair(t)

	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		t.Fatalf("Failed to parse PKCS1 private key: %v", err)
	}

	if key == nil {
		t.Fatal("Parsed key is nil")
	}

	if key.Size() != 256 { // 2048 bits = 256 bytes
		t.Errorf("Expected key size 256, got %d", key.Size())
	}
}

func TestParsePrivateKey_PKCS8(t *testing.T) {
	// Generate RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	// Encode to PKCS8
	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to marshal PKCS8 private key: %v", err)
	}

	privateKeyBlock := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKeyBytes,
	}
	privateKeyPEM := string(pem.EncodeToMemory(privateKeyBlock))

	// Parse it
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		t.Fatalf("Failed to parse PKCS8 private key: %v", err)
	}

	if key == nil {
		t.Fatal("Parsed key is nil")
	}
}

func TestParsePrivateKey_InvalidPEM(t *testing.T) {
	_, err := parsePrivateKey("not a valid PEM")
	if err == nil {
		t.Error("Expected error for invalid PEM")
	}
}

func TestParsePrivateKey_InvalidKeyData(t *testing.T) {
	invalidPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: []byte("invalid data"),
	})

	_, err := parsePrivateKey(string(invalidPEM))
	if err == nil {
		t.Error("Expected error for invalid key data")
	}
}

func TestNewCryptoModule(t *testing.T) {
	privateKeyPEM, _ := generateTestRSAKeyPair(t)

	// Set environment variable
	os.Setenv("HOWK_LUA_CRYPTO_TEST_KEY", privateKeyPEM)
	defer os.Unsetenv("HOWK_LUA_CRYPTO_TEST_KEY")

	cm, err := NewCryptoModule()
	if err != nil {
		t.Fatalf("Failed to create crypto module: %v", err)
	}

	if cm.KeyCount() != 1 {
		t.Errorf("Expected 1 key, got %d", cm.KeyCount())
	}

	if !cm.HasKey("TEST_KEY") {
		t.Error("Expected to have key TEST_KEY")
	}
}

func TestNewCryptoModule_NoKeys(t *testing.T) {
	// Ensure no HOWK_LUA_CRYPTO_ env vars are set
	for _, env := range os.Environ() {
		if len(env) > 16 && env[:16] == "HOWK_LUA_CRYPTO_" {
			parts := splitEnv(env)
			os.Unsetenv(parts[0])
		}
	}

	cm, err := NewCryptoModule()
	if err != nil {
		t.Fatalf("Failed to create crypto module: %v", err)
	}

	if cm.KeyCount() != 0 {
		t.Errorf("Expected 0 keys, got %d", cm.KeyCount())
	}
}

func TestNewCryptoModule_InvalidKey(t *testing.T) {
	// Set invalid key
	os.Setenv("HOWK_LUA_CRYPTO_INVALID", "not a valid key")
	defer os.Unsetenv("HOWK_LUA_CRYPTO_INVALID")

	_, err := NewCryptoModule()
	if err == nil {
		t.Error("Expected error for invalid key")
	}
}

func TestCryptoModule_DecryptCredential_Success(t *testing.T) {
	// Generate key pair
	privateKeyPEM, publicKeyPEM := generateTestRSAKeyPair(t)

	// Parse public key for encryption
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		t.Fatal("Failed to decode public key PEM")
	}
	publicKeyInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse public key: %v", err)
	}
	publicKey := publicKeyInterface.(*rsa.PublicKey)

	// Set up crypto module
	os.Setenv("HOWK_LUA_CRYPTO_TEST", privateKeyPEM)
	defer os.Unsetenv("HOWK_LUA_CRYPTO_TEST")

	cm, err := NewCryptoModule()
	if err != nil {
		t.Fatalf("Failed to create crypto module: %v", err)
	}

	// Encrypt some data
	plaintext := `{"token":"secret123","expires_at":"2026-01-01T00:00:00Z"}`
	encryptedKeyB64, encryptedDataB64 := encryptWithOAEP(t, publicKey, plaintext)

	// Decrypt using the module
	decrypted, err := cm.decryptData("TEST", encryptedKeyB64, encryptedDataB64)
	if err != nil {
		t.Fatalf("Failed to decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Expected %q, got %q", plaintext, decrypted)
	}
}

func TestCryptoModule_DecryptCredential_KeyNotFound(t *testing.T) {
	cm := &CryptoModule{keys: make(map[string]*rsa.PrivateKey)}

	_, err := cm.decryptData("NONEXISTENT", "key", "data")
	if err == nil {
		t.Error("Expected error for nonexistent key")
	}
}

func TestCryptoModule_DecryptCredential_InvalidBase64(t *testing.T) {
	privateKeyPEM, _ := generateTestRSAKeyPair(t)

	os.Setenv("HOWK_LUA_CRYPTO_TEST", privateKeyPEM)
	defer os.Unsetenv("HOWK_LUA_CRYPTO_TEST")

	cm, err := NewCryptoModule()
	if err != nil {
		t.Fatalf("Failed to create crypto module: %v", err)
	}

	// Test invalid symmetric key base64
	_, err = cm.decryptData("TEST", "not-valid-base64!!!", "data")
	if err == nil {
		t.Error("Expected error for invalid symmetric key base64")
	}

	// Test invalid data base64
	_, err = cm.decryptData("TEST", base64.StdEncoding.EncodeToString([]byte("test")), "not-valid-base64!!!")
	if err == nil {
		t.Error("Expected error for invalid data base64")
	}
}

func TestCryptoModule_DecryptCredential_WrongKey(t *testing.T) {
	// Generate two different key pairs
	privateKeyPEM1, publicKeyPEM1 := generateTestRSAKeyPair(t)
	privateKeyPEM2, _ := generateTestRSAKeyPair(t)

	// Parse first public key for encryption
	block, _ := pem.Decode([]byte(publicKeyPEM1))
	if block == nil {
		t.Fatal("Failed to decode public key PEM")
	}
	publicKeyInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse public key: %v", err)
	}
	publicKey := publicKeyInterface.(*rsa.PublicKey)

	// Set up crypto module with second private key (wrong key)
	os.Setenv("HOWK_LUA_CRYPTO_KEY1", privateKeyPEM1)
	os.Setenv("HOWK_LUA_CRYPTO_KEY2", privateKeyPEM2)
	defer os.Unsetenv("HOWK_LUA_CRYPTO_KEY1")
	defer os.Unsetenv("HOWK_LUA_CRYPTO_KEY2")

	cm, err := NewCryptoModule()
	if err != nil {
		t.Fatalf("Failed to create crypto module: %v", err)
	}

	// Encrypt with first key's public key
	plaintext := "secret data"
	encryptedKeyB64, encryptedDataB64 := encryptWithOAEP(t, publicKey, plaintext)

	// Try to decrypt with second key (should fail)
	_, err = cm.decryptData("KEY2", encryptedKeyB64, encryptedDataB64)
	if err == nil {
		t.Error("Expected error when decrypting with wrong key")
	}
}

func TestCryptoModule_DecryptCredential_CorruptedData(t *testing.T) {
	privateKeyPEM, publicKeyPEM := generateTestRSAKeyPair(t)

	// Parse public key
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		t.Fatal("Failed to decode public key PEM")
	}
	publicKeyInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse public key: %v", err)
	}
	publicKey := publicKeyInterface.(*rsa.PublicKey)

	// Set up crypto module
	os.Setenv("HOWK_LUA_CRYPTO_TEST", privateKeyPEM)
	defer os.Unsetenv("HOWK_LUA_CRYPTO_TEST")

	cm, err := NewCryptoModule()
	if err != nil {
		t.Fatalf("Failed to create crypto module: %v", err)
	}

	// Encrypt with valid key
	plaintext := "secret"
	encryptedKeyB64, _ := encryptWithOAEP(t, publicKey, plaintext)

	// Try to decrypt with corrupted data
	_, err = cm.decryptData("TEST", encryptedKeyB64, base64.StdEncoding.EncodeToString([]byte("corrupted")))
	if err == nil {
		t.Error("Expected error for corrupted data")
	}
}

// Helper to decrypt data directly (for testing)
func (cm *CryptoModule) decryptData(keySuffix, symmetricKeyB64, encryptedDataB64 string) (string, error) {
	privateKey, ok := cm.keys[keySuffix]
	if !ok {
		return "", fmt.Errorf("crypto key not found: %s", keySuffix)
	}

	// Decode base64
	encryptedKey, err := base64.StdEncoding.DecodeString(symmetricKeyB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode symmetric key: %w", err)
	}

	encryptedData, err := base64.StdEncoding.DecodeString(encryptedDataB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode encrypted data: %w", err)
	}

	// Decrypt AES key with RSA-OAEP
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, encryptedKey, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt AES key: %w", err)
	}

	// Decrypt data with AES-GCM
	plaintext, err := decryptAESGCM(aesKey, encryptedData)
	if err != nil {
		return "", err
	}

	return plaintext, nil
}

// Helper function
func splitEnv(env string) []string {
	for i := 0; i < len(env); i++ {
		if env[i] == '=' {
			return []string{env[:i], env[i+1:]}
		}
	}
	return []string{env}
}
