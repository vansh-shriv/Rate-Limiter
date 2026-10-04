package limiter

// Key builds the Redis key for a (tenant, algorithm, subject) triple.
// The {tenant} hash tag keeps all of a tenant's keys in one Redis Cluster slot.
func Key(tenant string, algo Algorithm, subject string) string {
	return "rl:{" + tenant + "}:" + string(algo) + ":" + subject
}
