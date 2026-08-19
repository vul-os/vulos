package appnet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// POSTINSTALL-04. post_install is where an app's own config file is written,
// and several recipes write the listening port into it. The installer did not
// export PORT, and `sh -c` expands an unset ${PORT} to the EMPTY STRING and
// exits 0 — so the recipe wrote a config with a hole in it, the installer
// reported success, and the app died at first launch with nothing logged.
//
// Two shipped recipes carried exactly that: nginx's post_install writes
// `listen ${PORT};` and transmission's writes `"rpc-port":${PORT}`.

// TestRunPostInstall_ExportsPortSoAConfigFileGetsANumber is the positive half:
// with a port declared, the recipe's ${PORT} must reach the file as the number
// the launcher will substitute into `command`, not as an empty string.
func TestRunPostInstall_ExportsPortSoAConfigFileGetsANumber(t *testing.T) {
	dir := t.TempDir()
	recipe := &VersionRecipe{
		Port:        9091,
		PostInstall: `printf 'rpc-port:%s\n' "${PORT}" > conf.txt`,
	}
	if err := runPostInstall(context.Background(), "transmission", "4", dir, recipe); err != nil {
		t.Fatalf("post_install failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "conf.txt"))
	if err != nil {
		t.Fatalf("config file was not written: %v", err)
	}
	if strings.TrimSpace(string(got)) != "rpc-port:9091" {
		t.Errorf("config got %q, want %q — ${PORT} did not reach post_install, so the "+
			"installed app's config names a port nothing listens on",
			strings.TrimSpace(string(got)), "rpc-port:9091")
	}
}

// TestRunPostInstall_PortMatchesTheRecipeFieldTheLauncherUses pins WHICH number
// is exported. A second opinion about the port — a default, a pool allocation,
// the host port — would make the config disagree with the command line, and the
// two would be wrong only at runtime.
func TestRunPostInstall_PortMatchesTheRecipeFieldTheLauncherUses(t *testing.T) {
	dir := t.TempDir()
	recipe := &VersionRecipe{
		Port:        8080,
		Command:     "bin/app --addr :${PORT}",
		PostInstall: `printf '%s' "${PORT}" > port.txt`,
	}
	if err := runPostInstall(context.Background(), "diwan", "0.1.0", dir, recipe); err != nil {
		t.Fatalf("post_install failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "port.txt"))
	if string(got) != "8080" {
		t.Errorf("exported PORT=%q, recipe declares port 8080 — config and `command` must name one number", got)
	}
}

// TestRunPostInstall_NoPortDeclaredExportsNothing keeps the negative case
// honest. With no port there is nothing true to export, so the empty expansion
// remains — which is precisely why validateRecipeSecurity refuses the
// combination before an install ever runs (the test below).
func TestRunPostInstall_NoPortDeclaredExportsNothing(t *testing.T) {
	dir := t.TempDir()
	recipe := &VersionRecipe{PostInstall: `printf '[%s]' "${PORT}" > port.txt`}
	if err := runPostInstall(context.Background(), "x", "1", dir, recipe); err != nil {
		t.Fatalf("post_install failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "port.txt"))
	if string(got) != "[]" {
		t.Errorf("got %q, want %q — this test states the untreated behaviour that "+
			"POSTINSTALL-04's validation exists to keep out of the registry", got, "[]")
	}
}

// TestValidateRecipe_RefusesPortReferenceWithoutAPort is the gate. It must fire
// on both spellings sh honours: a rule that knew only ${PORT} would be walked
// around by accident the first time someone wrote $PORT.
func TestValidateRecipe_RefusesPortReferenceWithoutAPort(t *testing.T) {
	cases := []struct {
		name        string
		postInstall string
		port        int
		wantRefused bool
	}{
		{"braced, no port", `printf 'listen ${PORT};' > nginx.conf`, 0, true},
		{"bare, no port", `printf 'listen $PORT;' > nginx.conf`, 0, true},
		{"braced, port declared", `printf 'listen ${PORT};' > nginx.conf`, 8080, false},
		{"bare, port declared", `printf 'listen $PORT;' > nginx.conf`, 8080, false},
		{"no reference, no port", `mkdir -p data`, 0, false},
		{"PORTFOLIO is not PORT", `printf '$PORTFOLIO' > x`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := rejectPortWithoutPort(c.postInstall, c.port)
			if c.wantRefused && err == nil {
				t.Fatal("accepted a post_install that expands ${PORT} to nothing — the install " +
					"would report success for a config file with a hole in it")
			}
			if !c.wantRefused && err != nil {
				t.Fatalf("refused a legitimate recipe: %v", err)
			}
		})
	}
}

// TestValidateRecipeSecurity_WiresThePortRule proves the check is REACHED from
// the function installs actually call. A helper nobody calls is the defect this
// codebase keeps finding; asserting the helper alone would reproduce it.
func TestValidateRecipeSecurity_WiresThePortRule(t *testing.T) {
	recipe := &VersionRecipe{
		FlatpakID:   "",
		PostInstall: `printf 'listen ${PORT};' > nginx.conf`,
		Port:        0,
		Command:     "bin/x",
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: "https://example.test/x.tar.gz", Checksum: strings.Repeat("a", 64)},
		},
	}
	err := validateRecipeSecurity(recipe)
	if err == nil || !strings.Contains(err.Error(), "POSTINSTALL-04") {
		t.Fatalf("validateRecipeSecurity did not apply the ${PORT} rule: %v", err)
	}
}
