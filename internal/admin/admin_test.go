package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ratelimiter/internal/tenant"
)

const token = "s3cret-admin"

func setup(t *testing.T) (http.Handler, string) {
	t.Helper()
	addr := os.Getenv("RL_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	store := tenant.NewStore(rdb)
	id := fmt.Sprintf("adm%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = store.Delete(context.Background(), id); _ = rdb.Close() })
	return New(store, token, slog.Default()), id
}

func call(h http.Handler, method, path, body, bearer string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestAdminRequiresToken(t *testing.T) {
	h, _ := setup(t)
	for _, bearer := range []string{"", "wrong", token + "x"} {
		if code, _ := call(h, "GET", "/admin/v1/tenants", "", bearer); code != 401 {
			t.Errorf("bearer %q: code=%d", bearer, code)
		}
	}
	if code, _ := call(h, "GET", "/admin/v1/tenants", "", token); code != 200 {
		t.Fatalf("valid token: code=%d", code)
	}
	// Non-Bearer scheme with the right secret is still rejected.
	req := httptest.NewRequest("GET", "/admin/v1/tenants", nil)
	req.Header.Set("Authorization", token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("raw token without Bearer: code=%d", rec.Code)
	}
}

func TestAdminTenantAndKeyFlow(t *testing.T) {
	h, id := setup(t)
	base := "/admin/v1/tenants/" + id
	body := `{"name":"Acme","rule":{"algorithm":"sliding_window","limit":50,"window":"1m"}}`

	if code, out := call(h, "PUT", base, body, token); code != 200 || !strings.Contains(out, `"window":"1m0s"`) {
		t.Fatalf("put: %d %s", code, out)
	}
	code, out := call(h, "GET", base, "", token)
	var got tenant.Tenant
	if json.Unmarshal([]byte(out), &got); code != 200 || got.Rule.Limit != 50 || got.Name != "Acme" {
		t.Fatalf("get: %d %s", code, out)
	}

	code, out = call(h, "POST", base+"/keys", "", token)
	var k struct {
		Key   string `json:"key"`
		KeyID string `json:"key_id"`
	}
	json.Unmarshal([]byte(out), &k)
	if code != 201 || !strings.HasPrefix(k.Key, "rlk_") || len(k.KeyID) != 16 {
		t.Fatalf("create key: %d %s", code, out)
	}
	// Listing keys exposes ids only, never plaintext.
	if code, out := call(h, "GET", base+"/keys", "", token); code != 200 || strings.Contains(out, k.Key) || !strings.Contains(out, k.KeyID) {
		t.Fatalf("list keys: %d %s", code, out)
	}
	if code, _ := call(h, "DELETE", base+"/keys/"+k.KeyID, "", token); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := call(h, "DELETE", base+"/keys/"+k.KeyID, "", token); code != 404 {
		t.Fatalf("revoke again: %d", code)
	}
	if code, _ := call(h, "DELETE", base, "", token); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(h, "GET", base, "", token); code != 404 {
		t.Fatalf("get after delete: %d", code)
	}
}

func TestAdminValidation(t *testing.T) {
	h, id := setup(t)
	base := "/admin/v1/tenants/" + id
	for name, tc := range map[string]struct{ path, body string }{
		"bad algorithm": {base, `{"rule":{"algorithm":"nope","limit":1,"window":"1s"}}`},
		"bad window":    {base, `{"rule":{"algorithm":"token_bucket","limit":1,"window":"soon"}}`},
		"zero limit":    {base, `{"rule":{"algorithm":"token_bucket","limit":0,"window":"1s"}}`},
		"unknown field": {base, `{"rule":{"algorithm":"token_bucket","limit":1,"window":"1s"},"x":1}`},
		"not json":      {base, `nope`},
		"bad id":        {"/admin/v1/tenants/bad%7Did", `{"rule":{"algorithm":"token_bucket","limit":1,"window":"1s"}}`},
	} {
		if code, out := call(h, "PUT", tc.path, tc.body, token); code != 400 {
			t.Errorf("%s: code=%d %s", name, code, out)
		}
	}
	if code, _ := call(h, "POST", "/admin/v1/tenants/ghost-tenant-zzz/keys", "", token); code != 404 {
		t.Errorf("key for unknown tenant: %d", code)
	}
}
