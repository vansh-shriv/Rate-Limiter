package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	srv := New(":0", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, 200}, {errors.New("down"), 503}} {
		srv := New(":0", func(context.Context) error { return tc.err })
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		if rec.Code != tc.want {
			t.Fatalf("err=%v got %d want %d", tc.err, rec.Code, tc.want)
		}
	}
}
