package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.RedisPoolSize != 100 || c.RedisTimeout != 200*time.Millisecond {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestBatchingDefaultsAndValidation(t *testing.T) {
	c, err := Load()
	if err != nil || c.BatchFlushers != 2 || c.BatchMax != 128 {
		t.Fatalf("defaults: %+v err=%v", c, err)
	}
	t.Setenv("RL_BATCH_FLUSHERS", "0") // 0 disables pipelining
	if c, err = Load(); err != nil || c.BatchFlushers != 0 {
		t.Fatalf("disable: %+v err=%v", c, err)
	}
	t.Setenv("RL_BATCH_FLUSHERS", "-1")
	if _, err = Load(); err == nil {
		t.Fatal("negative flushers must be rejected")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("RL_REDIS_ADDR", "redis:6379")
	t.Setenv("RL_REDIS_POOL_SIZE", "50")
	t.Setenv("RL_REDIS_TIMEOUT", "1s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RedisAddr != "redis:6379" || c.RedisPoolSize != 50 || c.RedisTimeout != time.Second {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestLoadInvalid(t *testing.T) {
	t.Setenv("RL_REDIS_POOL_SIZE", "abc")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
