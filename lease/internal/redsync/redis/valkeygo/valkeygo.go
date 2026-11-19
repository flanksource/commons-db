package valkeygo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	redsyncredis "github.com/flanksource/commons-db/lease/internal/redsync/redis"

	"github.com/valkey-io/valkey-go/valkeycompat"
)

type pool struct {
	delegate valkeycompat.Cmdable
}

func (p *pool) Get(ctx context.Context) (redsyncredis.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return &conn{p.delegate, ctx}, nil
}

// NewPool returns a rueidis-based pool implementation.
func NewPool(delegate valkeycompat.Cmdable) redsyncredis.Pool {
	return &pool{delegate}
}

type conn struct {
	delegate valkeycompat.Cmdable
	ctx      context.Context
}

func (c *conn) Get(name string) (string, error) {
	value, err := c.delegate.Get(c.ctx, name).Result()
	return value, noErrNil(err)
}

func (c *conn) Set(name string, value string) (bool, error) {
	reply, err := c.delegate.Set(c.ctx, name, value, 0).Result()
	return reply == "OK", err
}

func (c *conn) SetNX(name string, value string, expiry time.Duration) (bool, error) {
	return c.delegate.SetNX(c.ctx, name, value, expiry).Result()
}

func (c *conn) PTTL(name string) (time.Duration, error) {
	return c.delegate.PTTL(c.ctx, name).Result()
}

func (c *conn) Eval(script *redsyncredis.Script, keysAndArgs ...any) (any, error) {
	keyCount := script.KeyCount
	if keyCount < 0 {
		if len(keysAndArgs) == 0 {
			return nil, fmt.Errorf("missing Lua script key count")
		}
		count, err := strconv.Atoi(fmt.Sprint(keysAndArgs[0]))
		if err != nil || count < 0 {
			return nil, fmt.Errorf("invalid Lua script key count %v", keysAndArgs[0])
		}
		keyCount, keysAndArgs = count, keysAndArgs[1:]
	}
	if keyCount > len(keysAndArgs) {
		return nil, fmt.Errorf("script needs %d keys, got %d arguments", keyCount, len(keysAndArgs))
	}
	keys := make([]string, keyCount)
	for i, arg := range keysAndArgs[:keyCount] {
		key, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("script key %d has type %T, expected string", i+1, arg)
		}
		keys[i] = key
	}
	args := keysAndArgs[keyCount:]

	v, err := c.delegate.EvalSha(c.ctx, script.Hash, keys, args...).Result()
	if err != nil && strings.Contains(err.Error(), "NOSCRIPT ") {
		v, err = c.delegate.Eval(c.ctx, script.Src, keys, args...).Result()
	}
	return v, noErrNil(err)
}

func (c *conn) ScriptLoad(script *redsyncredis.Script) error {
	v, err := c.delegate.ScriptLoad(c.ctx, script.Src).Result()
	if err == nil {
		script.Hash = v
	}
	return err
}

func (c *conn) Close() error {
	// Not needed for this library
	return nil
}

func noErrNil(err error) error {
	if err == valkeycompat.Nil {
		return nil
	}
	return err
}
