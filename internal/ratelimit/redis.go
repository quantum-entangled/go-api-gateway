package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLimiter implements Limiter against a shared Redis instance, so
// multiple gateway processes share one token bucket per key. Prefix
// isolates the key space per service when multiple limiters share a client.
type RedisLimiter struct {
	client *redis.Client
	prefix string
	r      float64
	b      int
	script *redis.Script
}

// PEXPIRE adds 1000 ms grace so keys don't churn for users who show up right
// at the full-refill boundary.
const limiterLuaScript = `
local key     = KEYS[1]
local rate    = tonumber(ARGV[1])
local burst   = tonumber(ARGV[2])
local now_ms  = tonumber(ARGV[3])

local bucket  = redis.call("HMGET", key, "tokens", "ts")
local tokens  = tonumber(bucket[1]) or burst
local last    = tonumber(bucket[2]) or now_ms

local elapsed = math.max(0, now_ms - last) / 1000
tokens = math.min(burst, tokens + elapsed * rate)

local allowed = 0
if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
end

redis.call("HSET", key, "tokens", tokens, "ts", now_ms)
redis.call("PEXPIRE", key, math.ceil(burst / rate * 1000) + 1000)

return allowed
`

// NewRedisLimiter creates a RedisLimiter that allows r requests per second
// per key with a burst of b. Prefix is prepended to every key before the
// Lua call, so services sharing one client do not collide.
func NewRedisLimiter(client *redis.Client, prefix string, r float64, b int) *RedisLimiter {
	return &RedisLimiter{
		client: client,
		prefix: prefix,
		r:      r,
		b:      b,
		script: redis.NewScript(limiterLuaScript),
	}
}

// Allow reports whether the request identified by key should be allowed.
func (rl *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	res, err := rl.script.Run(ctx, rl.client, []string{rl.prefix + key}, rl.r, rl.b, time.Now().UnixMilli()).Int()
	if err != nil {
		return false, err
	}
	return res != 0, nil
}
