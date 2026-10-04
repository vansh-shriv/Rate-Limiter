package tenant

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"

	"ratelimiter/internal/auth"
	"ratelimiter/internal/rules"
)

const (
	tenantsSet = "rlcfg:tenants" // set of tenant ids
	eventsChan = "rlcfg:events"  // pub/sub: payload is a tenant id, or "*" for "flush everything"
	flushAll   = "*"
	keyPrefix  = "rlk_"
)

// ErrKeyNotFound is returned when revoking a key that does not belong to the tenant.
var ErrKeyNotFound = errors.New("tenant: api key not found")

func docKey(id string) string        { return "rlcfg:{" + id + "}:doc" }
func keysKey(id string) string       { return "rlcfg:{" + id + "}:keys" } // hash: keyID -> key hash
func apiKeyIndex(hash string) string { return "rlcfg:apikey:" + hash }    // string: tenant id

// Store persists tenants and API keys. Only SHA-256 hashes of API keys are stored.
type Store struct{ rdb *redis.Client }

func NewStore(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

// Put creates or replaces a tenant, then notifies all instances.
func (s *Store) Put(ctx context.Context, t Tenant) (Tenant, error) {
	if err := t.Validate(); err != nil {
		return Tenant{}, err
	}
	t.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	b, err := json.Marshal(t)
	if err != nil {
		return Tenant{}, err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, docKey(t.ID), b, 0)
	pipe.SAdd(ctx, tenantsSet, t.ID)
	pipe.Publish(ctx, eventsChan, t.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return Tenant{}, fmt.Errorf("put tenant: %w", err)
	}
	return t, nil
}

func (s *Store) Get(ctx context.Context, id string) (Tenant, error) {
	if !ValidID(id) {
		return Tenant{}, rules.ErrNotFound
	}
	b, err := s.rdb.Get(ctx, docKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Tenant{}, rules.ErrNotFound
	}
	if err != nil {
		return Tenant{}, fmt.Errorf("get tenant: %w", err)
	}
	var t Tenant
	if err := json.Unmarshal(b, &t); err != nil {
		return Tenant{}, fmt.Errorf("decode tenant %s: %w", id, err)
	}
	return t, nil
}

func (s *Store) List(ctx context.Context) ([]Tenant, error) {
	ids, err := s.rdb.SMembers(ctx, tenantsSet).Result()
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	sort.Strings(ids)
	out := make([]Tenant, 0, len(ids))
	for _, id := range ids {
		t, err := s.Get(ctx, id)
		if errors.Is(err, rules.ErrNotFound) {
			continue // deleted between SMEMBERS and GET
		}
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Delete removes a tenant and revokes all of its API keys.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	keys, err := s.rdb.HGetAll(ctx, keysKey(id)).Result()
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	pipe := s.rdb.TxPipeline()
	for _, hash := range keys {
		pipe.Del(ctx, apiKeyIndex(hash))
	}
	pipe.Del(ctx, docKey(id), keysKey(id))
	pipe.SRem(ctx, tenantsSet, id)
	pipe.Publish(ctx, eventsChan, flushAll)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	return nil
}

// CreateKey mints a new API key for the tenant. The plaintext is returned exactly once.
func (s *Store) CreateKey(ctx context.Context, id string) (raw, keyID string, err error) {
	if _, err := s.Get(ctx, id); err != nil {
		return "", "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = keyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	hash := auth.HashKey(raw)
	keyID = auth.KeyID(hash)

	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, keysKey(id), keyID, hash)
	pipe.Set(ctx, apiKeyIndex(hash), id, 0)
	pipe.Publish(ctx, eventsChan, flushAll) // flush negative cache entries for the new key
	if _, err := pipe.Exec(ctx); err != nil {
		return "", "", fmt.Errorf("create key: %w", err)
	}
	return raw, keyID, nil
}

func (s *Store) RevokeKey(ctx context.Context, id, keyID string) error {
	hash, err := s.rdb.HGet(ctx, keysKey(id), keyID).Result()
	if errors.Is(err, redis.Nil) {
		return ErrKeyNotFound
	}
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, apiKeyIndex(hash))
	pipe.HDel(ctx, keysKey(id), keyID)
	pipe.Publish(ctx, eventsChan, flushAll)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	return nil
}

func (s *Store) ListKeys(ctx context.Context, id string) ([]string, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	ids, err := s.rdb.HKeys(ctx, keysKey(id)).Result()
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

// LookupKey maps an API key hash to its tenant id (rules.ErrNotFound if unknown).
func (s *Store) LookupKey(ctx context.Context, hash string) (string, error) {
	id, err := s.rdb.Get(ctx, apiKeyIndex(hash)).Result()
	if errors.Is(err, redis.Nil) {
		return "", rules.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lookup key: %w", err)
	}
	return id, nil
}
