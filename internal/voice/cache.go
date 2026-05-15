package voice

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type TrackCache struct {
	rdb *redis.Client
}

func NewTrackCache(rdb *redis.Client) *TrackCache {
	return &TrackCache{rdb: rdb}
}

type TrackInfo struct {
	Title    string
	Uploader string
}

var trackCache *TrackCache

func InitTrackCache(c *TrackCache) {
	if c == nil {
		panic("trackCache is nil")
	}

	trackCache = c
}

func (c *TrackCache) Save(ctx context.Context, id string, title string, uploader string) error {
	data, _ := json.Marshal(TrackInfo{
		Title:    title,
		Uploader: uploader,
	})

	return c.rdb.Set(ctx, "track_"+id, data, time.Minute*10).Err()
}

func (c *TrackCache) Get(ctx context.Context, id string) (TrackInfo, error) {
	var t TrackInfo

	val, err := c.rdb.Get(ctx, "track_"+id).Result()
	if err != nil {
		return t, err
	}

	err = json.Unmarshal([]byte(val), &t)
	return t, err
}
