package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/lease"
	"github.com/flanksource/commons-db/lease/redis/internal/redsync"
	redispool "github.com/flanksource/commons-db/lease/redis/internal/redsync/redis"
	"github.com/flanksource/commons-db/lease/redis/internal/redsync/redis/valkeygo"
	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeycompat"
)

// New uses one authoritative Redis or Valkey primary per key.
func New(client valkey.Client, options lease.ExpiringOptions) (lease.Locker, error) {
	if client == nil {
		return nil, errors.New("a Redis or Valkey client is required")
	}
	options, err := lease.NormalizeExpiring(options)
	if err != nil {
		return nil, err
	}
	backend := newRedisBackend(client, options.TTL)
	return &lease.Backend{Options: options.Options, TTL: options.TTL, Begin: backend.begin, Holders: backend.holders}, nil
}

func newRedisBackend(client valkey.Client, ttl time.Duration) *redisBackend {
	return &redisBackend{client: client, pool: valkeygo.NewPool(valkeycompat.NewAdapter(client)), ttl: ttl}
}

// redisKeys are the writer, shared holders, waiting writers, grouped holder
// index and holder names of a key, all in one hash slot.
func redisKeys(req lease.Request) []string {
	prefix := "commons-db:lease:{" + req.Resource() + "}:"
	return []string{prefix + "writer", prefix + "shared", prefix + "waiting", prefix + "grouped", prefix + "holders"}
}

type redisBackend struct {
	client valkey.Client
	pool   redispool.Pool
	ttl    time.Duration
}

type redisResource struct {
	*redisBackend
	keys []string // Writer, shared holders, waiting writers, grouped holders, holder names.
}

type redisAcquisition struct {
	redisResource
	lease.Request
	share, waiter  string
	registered     bool
	uncertainShare bool
	uncertainToken string
}

func (b *redisBackend) begin(req lease.Request, wait bool) lease.Acquisition {
	a := &redisAcquisition{redisResource: redisResource{b, redisKeys(req)}, Request: req}
	if req.Mode == lease.WriteMode {
		if wait {
			a.waiter = uuid.NewString()
		}
		return a
	}
	a.share = uuid.NewString()
	if req.Mode == lease.GroupMode {
		a.share = req.Group + ":" + a.share
	}
	return a
}

func (a *redisAcquisition) record() (string, error) {
	data, err := json.Marshal(lease.Holder{Name: a.Holder, Mode: a.Mode.String(), Group: a.Group, Since: time.Now().UTC()})
	return string(data), err
}

func (a *redisAcquisition) Try(ctx context.Context) (lease.Ownership, error) {
	record, err := a.record()
	if err != nil {
		return nil, err
	}
	if a.share == "" {
		return a.tryWriter(ctx, record)
	}
	a.uncertainShare = true
	if err := a.run(ctx, "share", a.share, a.Group, record); err != nil {
		a.uncertainShare = err != lease.ErrBusy
		return nil, err
	}
	a.uncertainShare = false
	return &redisShared{a.redisResource, a.share}, nil
}

func (a *redisAcquisition) tryWriter(ctx context.Context, record string) (lease.Ownership, error) {
	pool := redisWriterPool{Pool: a.pool, resource: a.redisResource, waiter: a.waiter, registered: a.registered, record: record}
	token := uuid.NewString()
	writer := redsync.New(pool).NewMutex(a.keys[0], redsync.WithExpiry(a.ttl), redsync.WithTimeoutFactor(1.0/3), redsync.WithGenValueFunc(func() (string, error) { return token, nil }))
	if err := writer.TryLockContext(ctx); err != nil {
		var taken *redsync.ErrTaken
		if errors.As(err, &taken) {
			a.registered = a.waiter != ""
			return nil, lease.ErrBusy
		}
		a.uncertainToken = token
		return nil, fmt.Errorf("acquire Redis writer: %w", err)
	}
	return redisWriter{a.redisResource, token}, nil
}

func (a *redisAcquisition) Abandon(ctx context.Context) error {
	var err error
	if a.uncertainToken != "" {
		err = (redisWriter{a.redisResource, a.uncertainToken}).Unlock(ctx)
	}
	if a.waiter != "" {
		err = errors.Join(err, a.run(ctx, "cancel", a.waiter))
	}
	if a.uncertainShare {
		err = errors.Join(err, a.run(ctx, "release", a.share))
	}
	return err
}

// redisWriter renews and releases through the lease script, so the writer
// key and its holder entry change together.
type redisWriter struct {
	redisResource
	token string
}

func (w redisWriter) Renew(ctx context.Context) error  { return w.run(ctx, "extend", w.token) }
func (w redisWriter) Unlock(ctx context.Context) error { return w.run(ctx, "unwrite", w.token) }

type redisShared struct {
	redisResource
	token string
}

func (h *redisShared) Renew(ctx context.Context) error  { return h.run(ctx, "renew", h.token) }
func (h *redisShared) Unlock(ctx context.Context) error { return h.run(ctx, "release", h.token) }

func (r redisResource) params(op string, args ...string) []string {
	return append([]string{op, fmt.Sprint(r.ttl.Milliseconds())}, args...)
}

func (r redisResource) run(ctx context.Context, op string, args ...string) error {
	status, err := redisScript.Exec(ctx, r.client, r.keys, r.params(op, args...)).AsInt64()
	if err != nil {
		return fmt.Errorf("run Redis lease operation %s: %w", op, err)
	}
	switch status {
	case 1:
		return nil
	case 0:
		return lease.ErrBusy
	case -1:
		return lease.ErrNotOwned
	default:
		return fmt.Errorf("unexpected Redis lease status %d for %s", status, op)
	}
}

func (b *redisBackend) holders(ctx context.Context, req lease.Request) ([]lease.Holder, error) {
	resource := redisResource{b, redisKeys(req)}
	records, err := redisScript.Exec(ctx, b.client, resource.keys, resource.params("holders", "")).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("run Redis lease operation holders: %w", err)
	}
	holders := make([]lease.Holder, 0, len(records))
	for _, record := range records {
		var holder lease.Holder
		if err := json.Unmarshal([]byte(record), &holder); err != nil || holder.Name == "" {
			return nil, fmt.Errorf("invalid holder record %q in %s", record, resource.keys[4])
		}
		holders = append(holders, holder)
	}
	return holders, nil
}

type redisWriterPool struct {
	redispool.Pool
	resource       redisResource
	waiter, record string
	registered     bool
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
	err := resource.run(c.ctx, "write", token, c.pool.waiter, registered, c.pool.record)
	if err == lease.ErrBusy {
		return false, nil
	}
	return err == nil, err
}
