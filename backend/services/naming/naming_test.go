package naming

import "testing"

func TestValidateIdent(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		minLen  int
		maxLen  int
		wantErr bool
	}{
		// valid single hyphens
		{"abc", "test", 2, 63, false},
		{"a1", "test", 2, 63, false},
		{"hello-world", "test", 2, 63, false},
		{"my-app-id", "test", 2, 63, false},
		{"a0b", "test", 2, 63, false},
		// valid single char (if minLen allows)
		{"a", "test", 1, 63, false},
		{"9", "test", 1, 63, false},
		// double hyphen forbidden
		{"my--app", "test", 2, 63, true},
		{"a--b", "test", 2, 63, true},
		// leading/trailing hyphen forbidden
		{"-abc", "test", 2, 63, true},
		{"abc-", "test", 2, 63, true},
		// uppercase forbidden
		{"MyApp", "test", 2, 63, true},
		{"ABC", "test", 2, 63, true},
		// spaces forbidden
		{"my app", "test", 2, 63, true},
		// too short
		{"a", "test", 2, 63, true},
		// too long (64 chars, max 63)
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test", 2, 63, true},
		// exactly at max
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test", 2, 63, false}, // 63 chars
		// empty
		{"", "test", 2, 63, true},
		// underscore forbidden
		{"my_app", "test", 2, 63, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIdent(tt.name, tt.kind, tt.minLen, tt.maxLen)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateIdent(%q) error=%v, wantErr=%v", tt.name, err, tt.wantErr)
			}
		})
	}
}
