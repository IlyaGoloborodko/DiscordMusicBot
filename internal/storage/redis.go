package storage

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisConfig struct {
	Addr        string
	MaxRetries  int
	DialTimeout time.Duration
	Timeout     time.Duration
}

func DefaultRedisConfig() RedisConfig {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "redis:6379"
	}
	return RedisConfig{
		Addr:        addr,
		MaxRetries:  3,
		DialTimeout: 5 * time.Second,
		Timeout:     3 * time.Second,
	}
}

func NewClient(ctx context.Context, cfg RedisConfig) (*redis.Client, error) {
	db := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		MaxRetries:   cfg.MaxRetries,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.Timeout,
		WriteTimeout: cfg.Timeout,
	})

	if err := db.Ping(ctx).Err(); err != nil {
		fmt.Printf("failed to connect to redis server: %s\n", err.Error())
		return nil, err
	}

	return db, nil
}
