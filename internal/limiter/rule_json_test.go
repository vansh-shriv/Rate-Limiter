package limiter

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRuleJSONRoundTrip(t *testing.T) {
	in := Rule{Algorithm: SlidingWindowAlgo, Limit: 50, Window: 90 * time.Second, Burst: 5}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"algorithm":"sliding_window","limit":50,"window":"1m30s","burst":5}` {
		t.Fatalf("got %s", b)
	}
	var out Rule
	if err := json.Unmarshal(b, &out); err != nil || out != in {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestRuleJSONBadWindow(t *testing.T) {
	var r Rule
	err := json.Unmarshal([]byte(`{"algorithm":"token_bucket","limit":1,"window":"soon"}`), &r)
	if !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("got %v", err)
	}
}
