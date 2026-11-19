package lease

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/lease/internal/redsync"
	redispool "github.com/flanksource/commons-db/lease/internal/redsync/redis"
	"github.com/flanksource/commons-db/lease/internal/redsync/redis/valkeygo"
	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeycompat"
)

// NewRedis uses one authoritative Redis or Valkey primary per key.
func NewRedis(client valkey.Client, options ExpiringOptions) (Locker, error) {
	if client == nil {
		return nil, errors.New("a Redis or Valkey client is required")
	}
	options, err := normalizeExpiring(options)
	if err != nil {
		return nil, err
	}
	backend := newRedisBackend(client, options.TTL)
	return &locker{options.Options, options.TTL, backend.begin}, nil
}

func newRedisBackend(client valkey.Client, ttl time.Duration) *redisBackend {
	return &redisBackend{client: client, pool: valkeygo.NewPool(valkeycompat.NewAdapter(client)), ttl: ttl}
}

type redisBackend struct {
	client valkey.Client
	pool   redispool.Pool
	ttl    time.Duration
}

type redisResource struct {
	*redisBackend
	keys []string // Writer, shared holders, waiting writers.
}

type redisAcquisition struct {
	redisResource
	share, group, waiter string
	registered           bool
	uncertainShare       bool
	uncertainWriter      *redsync.Mutex
}

func (b *redisBackend) begin(req request, wait bool) acquisition {
	prefix := "commons-db:lease:{" + req.key + "}:"
	a := &redisAcquisition{redisResource: redisResource{b, []string{prefix + "writer", prefix + "shared", prefix + "waiting"}}}
	if req.mode == writeMode {
		if wait {
			a.waiter = uuid.NewString()
		}
		return a
	}
	a.share = uuid.NewString()
	if req.mode == groupMode {
		a.group = req.group
		a.share = req.group + ":" + a.share
	}
	return a
}

func (a *redisAcquisition) try(ctx context.Context) (ownership, error) {
	if a.share == "" {
		return a.tryWriter(ctx)
	}
	a.uncertainShare = true
	if err := a.run(ctx, "share", a.share, a.group); err != nil {
		a.uncertainShare = err != errBusy
		return nil, err
	}
	a.uncertainShare = false
	return &redisShared{a.redisResource, a.share}, nil
}

func (a *redisAcquisition) tryWriter(ctx context.Context) (ownership, error) {
	pool := redisWriterPool{Pool: a.pool, resource: a.redisResource, waiter: a.waiter, registered: a.registered}
	rs := redsync.New(pool)
	token := uuid.NewString()
	writer := rs.NewMutex(a.keys[0], redsync.WithExpiry(a.ttl), redsync.WithTimeoutFactor(1.0/3), redsync.WithGenValueFunc(func() (string, error) { return token, nil }))
	if err := writer.TryLockContext(ctx); err != nil {
		var taken *redsync.ErrTaken
		if errors.As(err, &taken) {
			a.registered = a.waiter != ""
			return nil, errBusy
		}
		a.uncertainWriter = rs.NewMutex(a.keys[0], redsync.WithValue(token))
		return nil, fmt.Errorf("acquire Redis writer: %w", err)
	}
	return redisWriter{writer}, nil
}

func (a *redisAcquisition) abandon(ctx context.Context) error {
	var err error
	if a.uncertainWriter != nil {
		err = (redisWriter{a.uncertainWriter}).unlock(ctx)
	}
	if a.waiter != "" {
		err = errors.Join(err, a.run(ctx, "cancel", a.waiter))
	}
	if a.uncertainShare {
		err = errors.Join(err, a.run(ctx, "release", a.share))
	}
	return err
}

type redisWriter struct{ mutex *redsync.Mutex }

func (w redisWriter) renew(ctx context.Context) error {
	ok, err := w.mutex.ExtendContext(ctx)
	if err != nil {
		var taken *redsync.ErrTaken
		if errors.As(err, &taken) {
			return errNotOwned
		}
		return err
	}
	if !ok || !time.Now().Before(w.mutex.Until()) {
		return errNotOwned
	}
	return nil
}

func (w redisWriter) unlock(ctx context.Context) error {
	_, err := w.mutex.UnlockContext(ctx)
	var taken *redsync.ErrTaken
	if errors.Is(err, redsync.ErrLockAlreadyExpired) || errors.As(err, &taken) {
		return nil
	}
	return err
}

type redisShared struct {
	redisResource
	token string
}

func (h *redisShared) renew(ctx context.Context) error  { return h.run(ctx, "renew", h.token) }
func (h *redisShared) unlock(ctx context.Context) error { return h.run(ctx, "release", h.token) }

func (r redisResource) run(ctx context.Context, op string, args ...string) error {
	params := append([]string{op, fmt.Sprint(r.ttl.Milliseconds())}, args...)
	status, err := redisScript.Exec(ctx, r.client, r.keys, params).AsInt64()
	if err != nil {
		return fmt.Errorf("run Redis lease operation %s: %w", op, err)
	}
	switch status {
	case 1:
		return nil
	case 0:
		return errBusy
	case -1:
		return errNotOwned
	default:
		return fmt.Errorf("unexpected Redis lease status %d for %s", status, op)
	}
}

type redisWriterPool struct {
	redispool.Pool
	resource   redisResource
	waiter     string
	registered bool
}

func (p redisWriterPool) Get(ctx context.Context) (redispool.Conn, error) {
	conn, err := p.Pool.Get(ctx)
	if err != nil {
		return nil, err
	}
	return redisWriterConn{Conn: conn, ctx: ctx, pool: p}, nil
}

type redisWriterConn struct {
	redispool.Conn
	ctx  context.Context
	pool redisWriterPool
}

func (c redisWriterConn) SetNX(key, token string, expiry time.Duration) (bool, error) {
	resource := c.pool.resource
	if key != resource.keys[0] || expiry != resource.ttl {
		return false, errors.New("writer key or expiry does not match the Redis lease resource")
	}
	registered := "0"
	if c.pool.registered {
		registered = "1"
	}
	err := resource.run(c.ctx, "write", token, c.pool.waiter, registered)
	if err == errBusy {
		return false, nil
	}
	return err == nil, err
}
