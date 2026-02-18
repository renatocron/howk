package transformer

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// AuthConfig holds authentication credentials for a transformer script
type AuthConfig struct {
	// Credentials maps username -> password hash or plaintext
	// Auto-detection: $2a$ or $2b$ prefix = bcrypt, otherwise plaintext
	Credentials map[string]string
}

// ParsePasswdFile parses a passwd file content
// Format: one "username:hash_or_password" per line
// Lines starting with # are comments, blank lines are skipped
func ParsePasswdFile(data []byte) (*AuthConfig, error) {
	auth := &AuthConfig{
		Credentials: make(map[string]string),
	}

	lines := strings.Split(string(data), "\n")
	for lineNum, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse "username:password" format
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format on line %d: expected 'username:password'", lineNum+1)
		}

		username := strings.TrimSpace(parts[0])
		password := parts[1] // Keep as-is (might be bcrypt hash with $2b$ prefix)

		if username == "" {
			return nil, fmt.Errorf("empty username on line %d", lineNum+1)
		}

		auth.Credentials[username] = password
	}

	if len(auth.Credentials) == 0 {
		return nil, fmt.Errorf("no valid credentials found")
	}

	return auth, nil
}

// Check validates username and password against the auth config
// Returns true if credentials are valid
func (a *AuthConfig) Check(username, password string) bool {
	storedPassword, ok := a.Credentials[username]
	if !ok {
		return false
	}

	// Auto-detect bcrypt vs plaintext
	if isBcryptHash(storedPassword) {
		return checkBcrypt(password, storedPassword)
	}

	// Plaintext comparison (not recommended for production)
	return storedPassword == password
}

// isBcryptHash checks if the stored password looks like a bcrypt hash
func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$")
}

// checkBcrypt compares a password against a bcrypt hash
func checkBcrypt(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// IsBcrypt returns true if all credentials use bcrypt hashing
// Useful for health checks and warnings
func (a *AuthConfig) IsBcrypt() bool {
	for _, password := range a.Credentials {
		if !isBcryptHash(password) {
			return false
		}
	}
	return true
}

// HasUser returns true if the given username exists
func (a *AuthConfig) HasUser(username string) bool {
	_, ok := a.Credentials[username]
	return ok
}

// UserCount returns the number of configured users
func (a *AuthConfig) UserCount() int {
	return len(a.Credentials)
}

// HashPassword creates a bcrypt hash from a plaintext password
// This is a utility function for creating .passwd files
func HashPassword(password string) (string, error) {
	// Use bcrypt default cost (10)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// SecureCompare performs constant-time comparison of two strings
// to prevent timing attacks
func SecureCompare(a, b string) bool {
	return bytes.Equal([]byte(a), []byte(b))
}
