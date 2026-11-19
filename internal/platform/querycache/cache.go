// Package querycache caches successful, read-only upstream queries. Hits never
// revalidate in the background. Values are serialized so callers cannot mutate
// another caller's results, including nested maps, slices and large numeric IDs.
package querycache

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const (
	defaultEntries = 256
	defaultBytes   = 16 * 1024 * 1024
)

type entry struct {
	key     string
	data    []byte
	expires time.Time
}

type flight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	finished bool
	data     []byte
	err      error
}

// Cache's zero value is ready to use. Each cache retains at most 256 entries and
// 16 MiB. It is local to its owning service/client and is cleared on restart.
type Cache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	flights map[string]*flight
	bytes   int
	now     func() time.Time
}

// Key hashes all dimensions, including credentials when supplied by the caller.
// Secrets and user queries are never kept as map keys or emitted to logs.
func Key(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *Cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Cache) remove(e *list.Element) {
	v := e.Value.(*entry)
	c.bytes -= len(v.data)
	delete(c.entries, v.key)
	c.lru.Remove(e)
}

func decode[T any](data []byte) (T, error) {
	var out T
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	err := d.Decode(&out)
	return out, err
}

// Load returns a fresh copy of a cached success, or joins one in-flight fetch.
// fetch chooses the lifetime after validating the response (zero skips caching).
// Errors are never cached. Canceling one waiter leaves other callers running;
// canceling the last waiter immediately cancels and detaches the upstream work.
func Load[T any](ctx context.Context, c *Cache, key string, fetch func(context.Context) (T, time.Duration, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	c.mu.Lock()
	if e := c.entries[key]; e != nil {
		v := e.Value.(*entry)
		if c.clock().Before(v.expires) {
			c.lru.MoveToFront(e)
			data := v.data
			c.mu.Unlock()
			return decode[T](data)
		}
		c.remove(e)
	}
	f := c.flights[key]
	if f == nil {
		if c.flights == nil {
			c.flights = make(map[string]*flight)
		}
		// Bound retained bookkeeping as well as stored responses. Host-specific
		// request limits still apply to an uncached overflow request.
		if len(c.flights) >= defaultEntries {
			c.mu.Unlock()
			v, _, err := fetch(ctx)
			if err != nil {
				return zero, err
			}
			data, err := json.Marshal(v)
			if err != nil {
				return zero, err
			}
			return decode[T](data)
		}
		callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = f
		go func() {
			defer cancel()
			value, ttl, err := fetch(callCtx)
			var data []byte
			if err == nil {
				data, err = json.Marshal(value)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.flights[key] == f {
				delete(c.flights, key)
				if err == nil && callCtx.Err() == nil && ttl > 0 && len(data) <= defaultBytes {
					now := c.clock()
					for e := c.lru.Back(); e != nil; {
						previous := e.Prev()
						if !now.Before(e.Value.(*entry).expires) {
							c.remove(e)
						}
						e = previous
					}
					for c.lru.Len() >= defaultEntries || c.bytes+len(data) > defaultBytes {
						c.remove(c.lru.Back())
					}
					if c.entries == nil {
						c.entries = make(map[string]*list.Element)
					}
					c.entries[key] = c.lru.PushFront(&entry{key, data, now.Add(ttl)})
					c.bytes += len(data)
				}
			}
			f.data, f.err, f.finished = data, err, true
			close(f.done)
		}()
	}
	f.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		f.waiters--
		if f.waiters == 0 && !f.finished {
			f.cancel()
			if c.flights[key] == f {
				delete(c.flights, key)
			}
		}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if f.err != nil {
			return zero, f.err
		}
		return decode[T](f.data)
	}
}
