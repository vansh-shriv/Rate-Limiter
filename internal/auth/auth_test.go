package auth

import (
	"net/http/httptest"
	"testing"
)

func TestExtractAPIKey(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if ExtractAPIKey(r) != "" {
		t.Fatal("want empty")
	}
	r.Header.Set("Authorization", "Bearer abc")
	if ExtractAPIKey(r) != "abc" {
		t.Fatal("bearer")
	}
	r.Header.Set("X-API-Key", "xyz")
	if ExtractAPIKey(r) != "xyz" {
		t.Fatal("x-api-key wins")
	}
}

func TestHeaderIdentifier(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "1.2.3.4:5555"
	id, _ := HeaderIdentifier{}.Identify(r)
	if id.Tenant != DefaultTenant || id.Subject != "ip:1.2.3.4" {
		t.Fatalf("%+v", id)
	}
	r.Header.Set("X-Tenant-ID", "acme")
	r.Header.Set("X-API-Key", "secret")
	id, _ = HeaderIdentifier{}.Identify(r)
	if id.Tenant != "acme" || id.Subject != "key:"+KeyID(HashKey("secret")) {
		t.Fatalf("%+v", id)
	}
	if id.Subject == "key:secret" {
		t.Fatal("raw key must never appear in subject")
	}
}
