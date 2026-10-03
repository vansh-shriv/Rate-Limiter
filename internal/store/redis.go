// Package store wraps the Redis client used by all limiters.
package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"ratelimiter/internal/config"
)

// NewRedis builds a pooled Redis client and verifies connectivity.
func NewRedis(ctx context.Context, c config.Config) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         c.RedisAddr,
		Password:     c.RedisPassword,
		DB:           c.RedisDB,
		PoolSize:     c.RedisPoolSize,
		MinIdleConns: c.RedisPoolSize / 10,
		DialTimeout:  c.RedisTimeout,
		ReadTimeout:  c.RedisTimeout,
		WriteTimeout: c.RedisTimeout,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping %s: %w", c.RedisAddr, err)
	}
	return rdb, nil
}
