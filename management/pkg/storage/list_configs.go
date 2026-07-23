// list_configs.go — ListConfigs for MemStore and SQLStore.
//
// ListConfigs is not part of the frozen Store contract (contract.go) but is
// required by the RunUsageSampler background job (STORE-05). Both MemStore
// and SQLStore implement it, satisfying the samplerStore interface in sampler.go.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// ListConfigs returns all storage configs, sorted by account_id.
// Implements the samplerStore interface used by RunUsageSampler.
func (m *MemStore) ListConfigs(_ context.Context) ([]Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Config, 0, len(m.cfgs))
	for _, c := range m.cfgs {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

// ListConfigs returns all storage configs from the database, sorted by account_id.
// Implements the samplerStore interface used by RunUsageSampler.
func (s *SQLStore) ListConfigs(ctx context.Context) ([]Config, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account_id, byo, endpoint, region, bucket, access_key, secret_key_enc
		   FROM storage_configs
		  ORDER BY account_id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list configs: %w", err)
	}
	defer rows.Close()

	var out []Config
	for rows.Next() {
		var (
			c            Config
			endpoint     sql.NullString
			accessKey    sql.NullString
			secretKeyEnc sql.NullString
			byoInt       int
		)
		if err := rows.Scan(&c.AccountID, &byoInt, &endpoint, &c.Region, &c.Bucket,
			&accessKey, &secretKeyEnc); err != nil {
			return nil, fmt.Errorf("storage: list configs scan: %w", err)
		}
		c.BYO = byoInt != 0
		c.Endpoint = endpoint.String
		c.AccessKey = accessKey.String
		// Decrypt secret key if KEK is set (BYO only).
		if secretKeyEnc.Valid && secretKeyEnc.String != "" && len(s.kek) == 32 {
			dec, decErr := aesGCMDecrypt(s.kek, secretKeyEnc.String)
			if decErr != nil {
				// Corrupted row: skip silently (sampler will see empty bucket and skip).
				continue
			}
			c.SecretKey = dec
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list configs rows: %w", err)
	}
	return out, nil
}
