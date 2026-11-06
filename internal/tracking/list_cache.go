package tracking

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

type listEntry struct{ body, gzip []byte }

// ListCache owns two immutable list versions. Requests never generate them.
type ListCache struct {
	store   *Store
	now     func() time.Time
	refresh sync.Mutex
	mu      sync.RWMutex
	entries map[string]listEntry
}

func NewListCache(store *Store, now func() time.Time) *ListCache {
	if now == nil {
		now = time.Now
	}
	return &ListCache{store: store, now: now, entries: map[string]listEntry{}}
}

// Refresh replaces each type independently after JSON and gzip both succeed.
func (c *ListCache) Refresh() error {
	c.refresh.Lock()
	defer c.refresh.Unlock()
	var failures []error
	for _, kind := range projectTypes {
		list, err := c.store.list(parameters{kind: kind}, c.now().UnixMilli())
		var body []byte
		var compressed bytes.Buffer
		if err == nil {
			body, err = json.Marshal(list)
		}
		if err == nil {
			writer := gzip.NewWriter(&compressed)
			_, err = writer.Write(body)
			closeErr := writer.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			log.Printf("list cache generation failed for %s", kind)
			failures = append(failures, err)
			continue
		}
		c.mu.Lock()
		c.entries[kind] = listEntry{body: body, gzip: compressed.Bytes()}
		c.mu.Unlock()
	}
	return errors.Join(failures...)
}

func (c *ListCache) get(kind string) (listEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[kind]
	return entry, ok
}

// Run is started by the server and ends before the Store is closed.
func (c *ListCache) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = c.Refresh()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func acceptsGzip(header string) bool {
	wildcard := false
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		coding := strings.ToLower(strings.TrimSpace(fields[0]))
		quality := 1.0
		for _, param := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(value, 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if coding == "gzip" {
			return quality > 0
		}
		if coding == "*" {
			wildcard = quality > 0
		}
	}
	return wildcard
}
