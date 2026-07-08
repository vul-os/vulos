package main

import (
	"net/http/httptest"
	"testing"
)

func TestMetricsAuthorized(t *testing.T) {
	ownerYes := func(id string) bool { return id == "owner" }

	cases := []struct {
		name   string
		token  string
		authHd string
		query  string
		userID string
		want   bool
	}{
		{"deny anonymous, no token configured", "", "", "", "", false},
		{"deny non-owner session", "", "", "", "rando", false},
		{"allow owner session", "", "", "", "owner", true},
		{"allow correct bearer token", "s3cret", "Bearer s3cret", "", "", true},
		{"allow correct query token", "s3cret", "", "s3cret", "", true},
		{"deny wrong bearer token", "s3cret", "Bearer nope", "", "", false},
		{"deny empty presented token even if configured", "s3cret", "", "", "", false},
		{"owner still works alongside a configured token", "s3cret", "", "", "owner", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/metrics", nil)
			if c.authHd != "" {
				req.Header.Set("Authorization", c.authHd)
			}
			if c.query != "" {
				q := req.URL.Query()
				q.Set("token", c.query)
				req.URL.RawQuery = q.Encode()
			}
			if c.userID != "" {
				req.Header.Set("X-User-ID", c.userID)
			}
			got := metricsAuthorized(req, c.token, ownerYes)
			if got != c.want {
				t.Fatalf("metricsAuthorized = %v, want %v", got, c.want)
			}
		})
	}
}
