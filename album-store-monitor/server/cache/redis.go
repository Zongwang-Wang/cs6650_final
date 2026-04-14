package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

// ── Prometheus counters for Redis cache hit/miss monitoring ───────────────────
var (
	cacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "album_store_cache_hits_total",
		Help: "Redis cache hits by operation (album_get, photo_get).",
	}, []string{"op"})

	cacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "album_store_cache_misses_total",
		Help: "Redis cache misses by operation — request fell through to DB.",
	}, []string{"op"})

	seqIncrTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "album_store_seq_incr_total",
		Help: "Total Redis INCR calls for per-album seq counter assignment.",
	})
)

type PhotoStatus struct {
	PhotoID string `json:"photo_id"`
	AlbumID string `json:"album_id"`
	Seq     int64  `json:"seq"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
}

type Album struct {
	AlbumID     string `json:"album_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
}

type Cache struct {
	client *redis.Client
}

func New(redisURL string) (*Cache, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opt)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &Cache{client: client}, nil
}

func (c *Cache) NextSeq(ctx context.Context, albumID string) (int64, error) {
	seqIncrTotal.Inc()
	return c.client.Incr(ctx, "album:"+albumID+":seq").Result()
}

// InitSeq sets the seq counter for an album only if it is not already set (or below maxSeq).
// Used at startup to warm counters from DB max(seq).
func (c *Cache) InitSeq(ctx context.Context, albumID string, maxSeq int64) error {
	key := "album:" + albumID + ":seq"
	script := `
local cur = redis.call('GET', KEYS[1])
if not cur or tonumber(cur) < tonumber(ARGV[1]) then
  redis.call('SET', KEYS[1], ARGV[1])
end
return redis.call('GET', KEYS[1])`
	return c.client.Eval(ctx, script, []string{key}, maxSeq).Err()
}

// SetAlbum caches album metadata. No TTL — albums are immutable once written.
func (c *Cache) SetAlbum(ctx context.Context, a *Album) {
	b, err := json.Marshal(a)
	if err != nil {
		return
	}
	c.client.Set(ctx, "album:"+a.AlbumID, b, 0)
}

// GetAlbum retrieves cached album. Returns nil, nil on miss.
func (c *Cache) GetAlbum(ctx context.Context, albumID string) (*Album, error) {
	b, err := c.client.Get(ctx, "album:"+albumID).Bytes()
	if errors.Is(err, redis.Nil) {
		cacheMisses.WithLabelValues("album_get").Inc()
		return nil, nil
	}
	if err != nil {
		cacheMisses.WithLabelValues("album_get").Inc()
		return nil, err
	}
	cacheHits.WithLabelValues("album_get").Inc()
	var a Album
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SetPhotoStatus caches photo status with 1h TTL.
func (c *Cache) SetPhotoStatus(ctx context.Context, ps *PhotoStatus) {
	b, err := json.Marshal(ps)
	if err != nil {
		return
	}
	c.client.Set(ctx, "photo:"+ps.PhotoID, b, time.Hour)
}

// GetPhotoStatus retrieves cached photo status. Returns nil, nil on miss.
func (c *Cache) GetPhotoStatus(ctx context.Context, photoID string) (*PhotoStatus, error) {
	b, err := c.client.Get(ctx, "photo:"+photoID).Bytes()
	if errors.Is(err, redis.Nil) {
		cacheMisses.WithLabelValues("photo_get").Inc()
		return nil, nil
	}
	if err != nil {
		cacheMisses.WithLabelValues("photo_get").Inc()
		return nil, err
	}
	cacheHits.WithLabelValues("photo_get").Inc()
	var ps PhotoStatus
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, err
	}
	return &ps, nil
}

// DeletePhotoStatus removes a photo from cache (called on DELETE).
func (c *Cache) DeletePhotoStatus(ctx context.Context, photoID string) {
	c.client.Del(ctx, "photo:"+photoID)
}
