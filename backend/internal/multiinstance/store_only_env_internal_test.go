package multiinstance

// Internal test for storeOnlyEnv (unexported) — pins the exact accepted set so a
// future accidental widening (e.g. adding "on") or a broken case-fold is caught.

import "testing"

func TestStoreOnlyEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"TRUE", true},
		{" Yes ", true},
		{"0", false},
		{"false", false},
		{"no", false},
		{"", false},
		{"on", false},
		{"garbage", false},
	}
	for _, c := range cases {
		t.Setenv("VULOS_STORE_ONLY", c.val)
		if got := storeOnlyEnv(); got != c.want {
			t.Errorf("storeOnlyEnv(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}
