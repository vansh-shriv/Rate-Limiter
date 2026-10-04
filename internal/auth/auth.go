// Package auth identifies who is making a request: which tenant's rule applies and which bucket.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
)

const DefaultTenant = "default"

var (
	ErrNoCredentials      = errors.New("auth: no credentials")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
)

// Identity says whose rule applies (Tenant) and which bucket within that tenant (Subject).
type Identity struct {
	Tenant  string
	Subject string
}

// Identifier derives an Identity from a request. Errors other than ErrNoCredentials /
// ErrInvalidCredentials are treated as backend failures by callers.
type Identifier interface {
	Identify(r *http.Request) (Identity, error)
}

// ExtractAPIKey reads the key from `X-API-Key` or `Authorization: Bearer`.
func ExtractAPIKey(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// HashKey returns the hex SHA-256 of an API key. Only hashes are ever stored or used in Redis keys.
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// KeyID is the short public identifier of a key (first 16 hex chars of its hash).
func KeyID(hash string) string { return hash[:16] }

// ClientIP is the TCP peer address. X-Forwarded-For is deliberately ignored: it is client-controlled.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// HeaderIdentifier trusts the X-Tenant-ID header. DEVELOPMENT ONLY: any client can claim any tenant.
type HeaderIdentifier struct{}

func (HeaderIdentifier) Identify(r *http.Request) (Identity, error) {
	id := Identity{Tenant: r.Header.Get("X-Tenant-ID")}
	if id.Tenant == "" {
		id.Tenant = DefaultTenant
	}
	if k := ExtractAPIKey(r); k != "" {
		id.Subject = "key:" + KeyID(HashKey(k))
	} else {
		id.Subject = "ip:" + ClientIP(r)
	}
	return id, nil
}
