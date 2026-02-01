package modules

import (
	"context"
	"fmt"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
	"github.com/redis/go-redis/v9"
)

// KVModule provides Redis-backed key-value storage for Lua scripts
type KVModule struct {
	client    *redis.Client
	configID  string
	namespace string
}

// NewKVModule creates a new KV module for a specific config_id
// The namespace is extracted from config_id by taking the part before the first ":"
// e.g., "music:10" -> namespace "music", keys stored as "kv:music:{key}"
func NewKVModule(client *redis.Client, configID string) *KVModule {
	namespace := extractNamespace(configID)
	return &KVModule{
		client:    client,
		configID:  configID,
		namespace: namespace,
	}
}

// extractNamespace extracts the namespace from config_id
// If config_id contains ":", takes the part before the first ":"
// Otherwise, uses the entire config_id
func extractNamespace(configID string) string {
	if idx := strings.Index(configID, ":"); idx != -1 {
		return configID[:idx]
	}
	return configID
}

// redisKey builds the full Redis key with namespace prefix
func (kv *KVModule) redisKey(key string) string {
	return fmt.Sprintf("kv:%s:%s", kv.namespace, key)
}

// LoadKV loads the kv module into the Lua state
func LoadKV(L *lua.LState, client *redis.Client, configID string) {
	kv := NewKVModule(client, configID)

	L.PreloadModule("kv", func(L *lua.LState) int {
		mod := L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
			"get": kv.get,
			"set": kv.set,
			"del": kv.del,
		})
		L.Push(mod)
		return 1
	})
}

// get retrieves a value from Redis
// Lua: value, err = kv.get(key)
func (kv *KVModule) get(L *lua.LState) int {
	key := L.CheckString(1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	val, err := kv.client.Get(ctx, kv.redisKey(key)).Result()
	if err == redis.Nil {
		// Key not found - return nil
		L.Push(lua.LNil)
		return 1
	}
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(fmt.Sprintf("kv.get error: %v", err)))
		return 2
	}

	L.Push(lua.LString(val))
	return 1
}

// set stores a value in Redis with optional TTL
// Lua: err = kv.set(key, value, ttl_secs)
// ttl_secs is optional (0 means no TTL)
func (kv *KVModule) set(L *lua.LState) int {
	key := L.CheckString(1)
	value := L.CheckString(2)
	ttlSecs := L.OptInt(3, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var err error
	if ttlSecs > 0 {
		err = kv.client.Set(ctx, kv.redisKey(key), value, time.Duration(ttlSecs)*time.Second).Err()
	} else {
		err = kv.client.Set(ctx, kv.redisKey(key), value, 0).Err()
	}

	if err != nil {
		L.Push(lua.LString(fmt.Sprintf("kv.set error: %v", err)))
		return 1
	}

	L.Push(lua.LNil)
	return 1
}

// del deletes a key from Redis
// Lua: err = kv.del(key)
func (kv *KVModule) del(L *lua.LState) int {
	key := L.CheckString(1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := kv.client.Del(ctx, kv.redisKey(key)).Err()
	if err != nil {
		L.Push(lua.LString(fmt.Sprintf("kv.del error: %v", err)))
		return 1
	}

	L.Push(lua.LNil)
	return 1
}
