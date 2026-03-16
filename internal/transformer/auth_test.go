//go:build !integration

package transformer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// --------------------------------------------------------------------
// ParsePasswdFile
// --------------------------------------------------------------------

func TestParsePasswdFile_Valid(t *testing.T) {
	data := []byte("alice:secret\nbob:password123\n")
	auth, err := ParsePasswdFile(data)
	require.NoError(t, err)
	require.NotNil(t, auth)
	assert.Equal(t, 2, auth.UserCount())
	assert.True(t, auth.HasUser("alice"))
	assert.True(t, auth.HasUser("bob"))
}

func TestParsePasswdFile_SkipsEmptyLinesAndComments(t *testing.T) {
	data := []byte("# this is a comment\n\nalice:secret\n\n# another comment\n")
	auth, err := ParsePasswdFile(data)
	require.NoError(t, err)
	assert.Equal(t, 1, auth.UserCount())
	assert.True(t, auth.HasUser("alice"))
}

func TestParsePasswdFile_InvalidFormat(t *testing.T) {
	data := []byte("alicewithoutcolon\n")
	_, err := ParsePasswdFile(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid format")
}

func TestParsePasswdFile_EmptyUsername(t *testing.T) {
	data := []byte(":somepassword\n")
	_, err := ParsePasswdFile(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty username")
}

func TestParsePasswdFile_NoCredentials(t *testing.T) {
	// Only comments and blank lines — no valid entries.
	data := []byte("# comment only\n\n")
	_, err := ParsePasswdFile(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no valid credentials")
}

func TestParsePasswdFile_BcryptHash(t *testing.T) {
	// Pre-generated bcrypt hash for "mysecret"
	hash, err := bcrypt.GenerateFromPassword([]byte("mysecret"), bcrypt.MinCost)
	require.NoError(t, err)

	data := []byte("alice:" + string(hash) + "\n")
	auth, err := ParsePasswdFile(data)
	require.NoError(t, err)
	assert.True(t, auth.Check("alice", "mysecret"))
}

// --------------------------------------------------------------------
// AuthConfig.Check
// --------------------------------------------------------------------

func TestAuthConfig_Check_PlaintextMatch(t *testing.T) {
	auth := &AuthConfig{
		Credentials: map[string]string{"alice": "secret"},
	}
	assert.True(t, auth.Check("alice", "secret"))
}

func TestAuthConfig_Check_PlaintextMismatch(t *testing.T) {
	auth := &AuthConfig{
		Credentials: map[string]string{"alice": "secret"},
	}
	assert.False(t, auth.Check("alice", "wrong"))
}

func TestAuthConfig_Check_BcryptMatch(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	require.NoError(t, err)

	auth := &AuthConfig{
		Credentials: map[string]string{"bob": string(hash)},
	}
	assert.True(t, auth.Check("bob", "correct"))
}

func TestAuthConfig_Check_BcryptMismatch(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	require.NoError(t, err)

	auth := &AuthConfig{
		Credentials: map[string]string{"bob": string(hash)},
	}
	assert.False(t, auth.Check("bob", "incorrect"))
}

func TestAuthConfig_Check_UnknownUser(t *testing.T) {
	auth := &AuthConfig{
		Credentials: map[string]string{"alice": "secret"},
	}
	assert.False(t, auth.Check("charlie", "secret"))
}

// --------------------------------------------------------------------
// IsBcrypt / HasUser / UserCount
// --------------------------------------------------------------------

func TestAuthConfig_IsBcrypt_AllBcrypt(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	require.NoError(t, err)

	auth := &AuthConfig{
		Credentials: map[string]string{
			"u1": string(hash),
			"u2": string(hash),
		},
	}
	assert.True(t, auth.IsBcrypt())
}

func TestAuthConfig_IsBcrypt_Mixed(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	require.NoError(t, err)

	auth := &AuthConfig{
		Credentials: map[string]string{
			"u1": string(hash),
			"u2": "plaintext",
		},
	}
	assert.False(t, auth.IsBcrypt())
}

func TestAuthConfig_IsBcrypt_AllPlaintext(t *testing.T) {
	auth := &AuthConfig{
		Credentials: map[string]string{"u1": "plain"},
	}
	assert.False(t, auth.IsBcrypt())
}

func TestAuthConfig_HasUser(t *testing.T) {
	auth := &AuthConfig{
		Credentials: map[string]string{"alice": "pw"},
	}
	assert.True(t, auth.HasUser("alice"))
	assert.False(t, auth.HasUser("bob"))
}

func TestAuthConfig_UserCount(t *testing.T) {
	auth := &AuthConfig{
		Credentials: map[string]string{"a": "1", "b": "2", "c": "3"},
	}
	assert.Equal(t, 3, auth.UserCount())
}

// --------------------------------------------------------------------
// HashPassword
// --------------------------------------------------------------------

func TestHashPassword_ProducesValidBcrypt(t *testing.T) {
	hash, err := HashPassword("mypassword")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$"),
		"hash should be a bcrypt string, got: %s", hash)

	// Verify the hash can be used to authenticate
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte("mypassword"))
	assert.NoError(t, err)
}

// --------------------------------------------------------------------
// SecureCompare
// --------------------------------------------------------------------

func TestSecureCompare_Equal(t *testing.T) {
	assert.True(t, SecureCompare("hello", "hello"))
}

func TestSecureCompare_NotEqual(t *testing.T) {
	assert.False(t, SecureCompare("hello", "world"))
}

func TestSecureCompare_DifferentLengths(t *testing.T) {
	assert.False(t, SecureCompare("short", "longer string"))
}

func TestSecureCompare_Empty(t *testing.T) {
	assert.True(t, SecureCompare("", ""))
	assert.False(t, SecureCompare("", "notempty"))
}
