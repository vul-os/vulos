package appnet

import "testing"

func TestParseSubdomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		host       string
		baseDomain string
		wantApp    string
		wantProf   string
		wantOK     bool
	}{
		// --- Happy path ---
		{
			name:       "happy path with profile",
			host:       "myapp--dev.01JABCDEF.vulos.local",
			baseDomain: "vulos.local",
			wantApp:    "myapp",
			wantProf:   "dev",
			wantOK:     true,
		},
		{
			name:       "happy path with hyphens in app name",
			host:       "my-cool-app--staging.01JABCDEF.example.com",
			baseDomain: "example.com",
			wantApp:    "my-cool-app",
			wantProf:   "staging",
			wantOK:     true,
		},
		{
			name:       "happy path with hyphens in profile name",
			host:       "calc--prod-us-east.01JABCDEF.vulos.local",
			baseDomain: "vulos.local",
			wantApp:    "calc",
			wantProf:   "prod-us-east",
			wantOK:     true,
		},
		// --- Default profile fallback (no --) ---
		{
			name:       "default profile when no separator",
			host:       "calculator.01JABCDEF.vulos.local",
			baseDomain: "vulos.local",
			wantApp:    "calculator",
			wantProf:   "default",
			wantOK:     true,
		},
		{
			name:       "default profile single label no ulid",
			host:       "notes.vulos.local",
			baseDomain: "vulos.local",
			wantApp:    "notes",
			wantProf:   "default",
			wantOK:     true,
		},
		// --- Port stripping ---
		{
			name:       "port stripped from host",
			host:       "myapp--qa.ulid123.vulos.local:8080",
			baseDomain: "vulos.local",
			wantApp:    "myapp",
			wantProf:   "qa",
			wantOK:     true,
		},
		{
			name:       "port stripped default profile",
			host:       "calculator.vulos.local:3000",
			baseDomain: "vulos.local",
			wantApp:    "calculator",
			wantProf:   "default",
			wantOK:     true,
		},
		// --- Case-insensitive host ---
		{
			name:       "uppercase host normalised",
			host:       "MyApp--Dev.ULID.Vulos.Local",
			baseDomain: "vulos.local",
			wantApp:    "myapp",
			wantProf:   "dev",
			wantOK:     true,
		},
		// --- Missing baseDomain suffix ---
		{
			name:       "host does not end in baseDomain",
			host:       "myapp.otherdomain.com",
			baseDomain: "vulos.local",
			wantOK:     false,
		},
		{
			name:       "empty base domain no match",
			host:       "myapp.vulos.local",
			baseDomain: "",
			wantOK:     false,
		},
		// --- Multi-"--" rejection ---
		{
			name:       "two separators rejected",
			host:       "app--profile--extra.ulid.vulos.local",
			baseDomain: "vulos.local",
			wantOK:     false,
		},
		{
			name:       "three separators rejected",
			host:       "a--b--c--d.ulid.vulos.local",
			baseDomain: "vulos.local",
			wantOK:     false,
		},
		// --- Invalid identifier characters ---
		{
			name:       "uppercase in app part",
			host:       "MyApp--dev.ulid.vulos.local",
			baseDomain: "vulos.local",
			// After lowercasing: myapp--dev → valid
			wantApp:  "myapp",
			wantProf: "dev",
			wantOK:   true,
		},
		{
			name:       "underscore in app name rejected",
			host:       "my_app--dev.ulid.vulos.local",
			baseDomain: "vulos.local",
			wantOK:     false,
		},
		{
			name:       "leading hyphen in app rejected",
			host:       "-app--dev.ulid.vulos.local",
			baseDomain: "vulos.local",
			wantOK:     false,
		},
		{
			name:       "trailing hyphen in profile rejected",
			host:       "app--dev-.ulid.vulos.local",
			baseDomain: "vulos.local",
			// label = "app--dev-" but the label ends at first dot: "app--dev-"
			// profile = "dev-" which has trailing hyphen → invalid
			wantOK: false,
		},
		{
			name:       "empty app segment rejected",
			host:       "--dev.ulid.vulos.local",
			baseDomain: "vulos.local",
			wantOK:     false,
		},
		{
			name:       "empty profile segment rejected",
			host:       "app--.ulid.vulos.local",
			baseDomain: "vulos.local",
			// app--.ulid.vulos.local → label before first dot = "app--"
			// split on --: app="" and profile="" → both invalid
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotApp, gotProf, gotOK := ParseSubdomain(tt.host, tt.baseDomain)
			if gotOK != tt.wantOK {
				t.Errorf("ParseSubdomain(%q, %q) ok=%v, want %v", tt.host, tt.baseDomain, gotOK, tt.wantOK)
				return
			}
			if !tt.wantOK {
				return
			}
			if gotApp != tt.wantApp {
				t.Errorf("ParseSubdomain(%q, %q) app=%q, want %q", tt.host, tt.baseDomain, gotApp, tt.wantApp)
			}
			if gotProf != tt.wantProf {
				t.Errorf("ParseSubdomain(%q, %q) profile=%q, want %q", tt.host, tt.baseDomain, gotProf, tt.wantProf)
			}
		})
	}
}

func TestNet01ValidIdent(t *testing.T) {
	t.Parallel()

	valid := []string{"a", "abc", "my-app", "app-v2", "123", "a1b2c3"}
	for _, s := range valid {
		if !net01ValidIdent(s) {
			t.Errorf("net01ValidIdent(%q) = false, want true", s)
		}
	}

	invalid := []string{"", "-app", "app-", "-", "App", "my_app", "app app", "APP"}
	for _, s := range invalid {
		if net01ValidIdent(s) {
			t.Errorf("net01ValidIdent(%q) = true, want false", s)
		}
	}
}
