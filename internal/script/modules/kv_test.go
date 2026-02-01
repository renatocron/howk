package modules

import (
	"testing"
)

func TestExtractNamespace(t *testing.T) {
	tests := []struct {
		configID  string
		expected  string
	}{
		{"music:10", "music"},
		{"music:10:extra", "music"},
		{"simple", "simple"},
		{"org:123", "org"},
		{"my-app:prod:instance-1", "my-app"},
		{"", ""},
		{":colon_at_start", ""},
	}

	for _, tt := range tests {
		result := extractNamespace(tt.configID)
		if result != tt.expected {
			t.Errorf("extractNamespace(%q) = %q, want %q", tt.configID, result, tt.expected)
		}
	}
}

func TestKVModule_redisKey(t *testing.T) {
	kv := NewKVModule(nil, "music:10")
	
	tests := []struct {
		input    string
		expected string
	}{
		{"token", "kv:music:token"},
		{"session:123", "kv:music:session:123"},
		{"", "kv:music:"},
	}

	for _, tt := range tests {
		result := kv.redisKey(tt.input)
		if result != tt.expected {
			t.Errorf("redisKey(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestKVModule_NamespaceVariations(t *testing.T) {
	tests := []struct {
		configID     string
		key          string
		expectedKey  string
	}{
		{"music:10", "token", "kv:music:token"},
		{"music:10:user", "token", "kv:music:token"},
		{"simple", "token", "kv:simple:token"},
		{"org:123", "session", "kv:org:session"},
		{"a:b:c:d", "key", "kv:a:key"},
	}

	for _, tt := range tests {
		kv := NewKVModule(nil, tt.configID)
		result := kv.redisKey(tt.key)
		if result != tt.expectedKey {
			t.Errorf("configID=%q, key=%q: expected %q, got %q", 
				tt.configID, tt.key, tt.expectedKey, result)
		}
	}
}
