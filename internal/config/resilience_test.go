package config

import (
	"testing"
	"time"
)

func TestResilienceDefaultsAndValidation(t *testing.T) {
	c, err := Load()
	if err != nil || c.BreakerFailures != 5 || c.BreakerCooldown != 2*time.Second || c.ShutdownDelay != 0 || c.UpstreamTimeout != 30*time.Second {
		t.Fatalf("defaults: %+v err=%v", c, err)
	}
	for _, kv := range [][2]string{
		{"RL_BREAKER_FAILURES", "-1"}, {"RL_BREAKER_COOLDOWN", "0s"}, {"RL_SHUTDOWN_DELAY", "-1s"}, {"RL_UPSTREAM_TIMEOUT", "0s"},
	} {
		t.Run(kv[0], func(t *testing.T) {
			t.Setenv(kv[0], kv[1])
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s must be rejected", kv[0], kv[1])
			}
		})
	}
	t.Setenv("RL_BREAKER_FAILURES", "0") // 0 disables the breaker and is valid
	if c, err = Load(); err != nil || c.BreakerFailures != 0 {
		t.Fatalf("disable: %+v err=%v", c, err)
	}
}
