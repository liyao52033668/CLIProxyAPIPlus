// Package home KV helpers extracted for Antigravity credits/replay compatibility.
package home

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type KVSetOptions struct {
	EX time.Duration
	PX time.Duration
	NX bool
	XX bool
}

func buildKVSetArgs(key string, value []byte, opts KVSetOptions) ([]any, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("home kv: key is empty")
	}
	if opts.EX > 0 && opts.PX > 0 {
		return nil, fmt.Errorf("home kv: EX and PX are mutually exclusive")
	}
	if opts.EX < 0 || opts.PX < 0 {
		return nil, fmt.Errorf("home kv: ttl must not be negative")
	}
	if opts.NX && opts.XX {
		return nil, fmt.Errorf("home kv: NX and XX are mutually exclusive")
	}

	args := []any{key, append([]byte(nil), value...)}
	if opts.EX > 0 {
		args = append(args, "EX", durationCeil(opts.EX, time.Second))
	}
	if opts.PX > 0 {
		args = append(args, "PX", durationCeil(opts.PX, time.Millisecond))
	}
	if opts.NX {
		args = append(args, "NX")
	}
	if opts.XX {
		args = append(args, "XX")
	}
	return args, nil
}

func (c *Client) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, false, errClient
	}
	raw, errGet := cmd.Get(ctx, key).Bytes()
	if errors.Is(errGet, redis.Nil) {
		return nil, false, nil
	}
	if errGet != nil {
		return nil, false, errGet
	}
	return append([]byte(nil), raw...), true, nil
}

func (c *Client) KVSet(ctx context.Context, key string, value []byte, opts KVSetOptions) (bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	args, errArgs := buildKVSetArgs(key, value, opts)
	if errArgs != nil {
		return false, errArgs
	}
	result, errSet := cmd.Do(ctx, append([]any{"SET"}, args...)...).Result()
	if errors.Is(errSet, redis.Nil) {
		return false, nil
	}
	if errSet != nil {
		return false, errSet
	}
	if result == nil {
		return false, nil
	}
	return true, nil
}

func (c *Client) KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	opts := KVSetOptions{NX: true}
	if ttl > 0 {
		opts.EX = ttl
	}
	return c.KVSet(ctx, key, value, opts)
}

// KVCompareAndSwap atomically replaces a value only when its current state matches the expected state.
func (c *Client) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	const script = `
local current = redis.call("GET", KEYS[1])
if ARGV[1] == "1" then
  if not current or current ~= ARGV[2] then
    return 0
  end
elseif current then
  return 0
end
local ttl = tonumber(ARGV[4])
if ttl and ttl > 0 then
  redis.call("SET", KEYS[1], ARGV[3], "PX", ttl)
else
  redis.call("SET", KEYS[1], ARGV[3])
end
return 1
`
	expectedFlag := "0"
	if expectedExists {
		expectedFlag = "1"
	}
	result, errEval := cmd.Eval(ctx, script, []string{key}, expectedFlag, expected, value, durationCeil(ttl, time.Millisecond)).Int64()
	if errEval != nil {
		return false, errEval
	}
	return result == 1, nil
}

func (c *Client) KVDel(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return 0, errClient
	}
	return cmd.Del(ctx, keys...).Result()
}

func (c *Client) KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	return cmd.Expire(ctx, key, ttl).Result()
}

func (c *Client) KVTTL(ctx context.Context, key string) (time.Duration, bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return 0, false, errClient
	}
	ttl, errTTL := cmd.TTL(ctx, key).Result()
	if errTTL != nil {
		return 0, false, errTTL
	}
	switch {
	case ttl <= -2*time.Second:
		return 0, false, nil
	case ttl == -1*time.Second:
		return 0, true, nil
	default:
		return ttl, true, nil
	}
}

func (c *Client) KVIncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return 0, errClient
	}
	return cmd.IncrBy(ctx, key, delta).Result()
}

func (c *Client) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	if len(keys) == 0 {
		return nil, nil, nil
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, nil, errClient
	}
	items, errMGet := cmd.MGet(ctx, keys...).Result()
	if errMGet != nil {
		return nil, nil, errMGet
	}
	values := make([][]byte, len(items))
	found := make([]bool, len(items))
	for i, item := range items {
		switch typed := item.(type) {
		case nil:
			continue
		case string:
			values[i] = []byte(typed)
			found[i] = true
		case []byte:
			values[i] = append([]byte(nil), typed...)
			found[i] = true
		default:
			return nil, nil, fmt.Errorf("home kv: unsupported MGET item type %T", item)
		}
	}
	return values, found, nil
}

func (c *Client) KVMSet(ctx context.Context, pairs map[string][]byte) error {
	if len(pairs) == 0 {
		return nil
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	keys := make([]string, 0, len(pairs))
	for key := range pairs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]any, 0, 1+len(keys)*2)
	args = append(args, "MSET")
	for _, key := range keys {
		args = append(args, key, append([]byte(nil), pairs[key]...))
	}
	return cmd.Do(ctx, args...).Err()
}

func durationCeil(value time.Duration, unit time.Duration) int64 {
	if value <= 0 || unit <= 0 {
		return 0
	}
	return int64((value + unit - 1) / unit)
}
