package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeObs struct {
	route    string
	code     int
	inFlight float64
	maxIn    float64
}

func (f *fakeObs) ObserveHTTP(route string, code int, _ time.Duration) { f.route, f.code = route, code }
func (f *fakeObs) InFlightAdd(d float64) {
	f.inFlight += d
	f.maxIn = max(f.maxIn, f.inFlight)
}

func TestInstrumentRecordsStatusAndInFlight(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		want int
	}{
		{"explicit", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, 429},
		{"implicit 200 via Write", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }, 200},
		{"nothing written", func(w http.ResponseWriter, r *http.Request) {}, 200},
	} {
		obs := &fakeObs{}
		h := instrument("check", obs, slog.Default(), tc.h)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/some/dynamic/path/123", nil))
		if obs.route != "check" || obs.code != tc.want {
			t.Errorf("%s: route=%q code=%d", tc.name, obs.route, obs.code)
		}
		if obs.inFlight != 0 || obs.maxIn != 1 {
			t.Errorf("%s: in-flight not balanced: now=%v max=%v", tc.name, obs.inFlight, obs.maxIn)
		}
	}
}

// ReverseProxy streams via http.ResponseController; the recorder must not hide Flush.
func TestInstrumentPreservesFlusher(t *testing.T) {
	var flushErr error
	h := instrument("gateway", nil, slog.Default(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if flushErr != nil {
		t.Fatalf("flush through recorder failed: %v", flushErr)
	}
}
