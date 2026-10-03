// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr        string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	RedisPoolSize   int
	RedisTimeout    time.Duration // dial/read/write timeout
	ShutdownTimeout time.Duration
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	var err error
	c := Config{
		HTTPAddr:      getStr("RL_HTTP_ADDR", ":8080"),
		RedisAddr:     getStr("RL_REDIS_ADDR", "localhost:6379"),
		RedisPassword: getStr("RL_REDIS_PASSWORD", ""),
	}
	if c.RedisDB, err = getInt("RL_REDIS_DB", 0); err != nil {
		return c, err
	}
	if c.RedisPoolSize, err = getInt("RL_REDIS_POOL_SIZE", 100); err != nil {
		return c, err
	}
	if c.RedisTimeout, err = getDur("RL_REDIS_TIMEOUT", 200*time.Millisecond); err != nil {
		return c, err
	}
	if c.ShutdownTimeout, err = getDur("RL_SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return c, err
	}
	return c, nil
}

func getStr(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getInt(k string, def int) (int, error) {
	v, ok := os.LookupEnv(k)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config %s: %w", k, err)
	}
	return n, nil
}

func getDur(k string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(k)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config %s: %w", k, err)
	}
	return d, nil
}
