package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// newFlagSet builds a flag set that reports parse errors to the caller (rather
// than calling os.Exit) so main can map them to the usage exit code.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("vulos web "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// runWeb dispatches the `vulos web …` subcommands.
func runWeb(args []string) error {
	if len(args) == 0 {
		return usagef("usage: vulos web <deploy|list|rm|domain> …")
	}
	switch args[0] {
	case "deploy":
		return webDeploy(args[1:])
	case "list", "ls":
		return webList(args[1:])
	case "rm", "delete":
		return webRm(args[1:])
	case "domain", "domains":
		return webDomain(args[1:])
	default:
		return usagef("unknown web subcommand %q", args[0])
	}
}

// ---- deploy ---------------------------------------------------------------

// deployResult mirrors POST /api/web/sites.
type deployResult struct {
	OK  bool   `json:"ok"`
	URL string `json:"url"`
}

func webDeploy(args []string) error {
	fs := newFlagSet("deploy")
	site := fs.String("site", "", "site name (defaults to the directory's base name)")
	if err := fs.Parse(args); err != nil {
		return usagef("%v", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return usagef("usage: vulos web deploy <dir> [--site NAME]")
	}
	dir := rest[0]

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	if !info.IsDir() {
		return usagef("%s is not a directory", dir)
	}

	name := *site
	if name == "" {
		name = deriveSiteName(dir)
	}
	if !validSiteName(name) {
		return usagef("invalid site name %q (use letters, digits, and dashes)", name)
	}

	// Package the built bundle in memory. Static sites are small; keeping the
	// gzip in a buffer lets us report the exact upload size and avoids a temp
	// file on the caller's disk.
	var buf bytes.Buffer
	n, err := tarGzDir(dir, &buf)
	if err != nil {
		return fmt.Errorf("packaging %s: %w", dir, err)
	}
	fmt.Printf("packaged %s (%d files, %s compressed)\n", dir, n, humanBytes(int64(buf.Len())))

	c, err := newClient()
	if err != nil {
		return err
	}

	// Contract: the bundle is sent as the raw request body with the site name in
	// the ?site= query param (the single-shot form documented in the WEB_API
	// contract; multipart is the alternative we did not take).
	path := "/api/web/sites?site=" + url.QueryEscape(name)
	var out deployResult
	if err := c.doJSON(http.MethodPost, path, &buf, "application/gzip", &out); err != nil {
		return err
	}
	fmt.Printf("deployed %q -> %s\n", name, out.URL)
	return nil
}

// tarGzDir writes a gzip-compressed tar of dir's contents into w and returns the
// number of regular files included. Dotfiles/dotdirs and node_modules are
// skipped so build output — not the developer's local cruft — is what ships.
func tarGzDir(dir string, w io.Writer) (int, error) {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	count := 0

	root := filepath.Clean(dir)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		base := d.Name()
		// Ignore dotfiles/dotdirs (.git, .env, .DS_Store, …) and dependency dirs.
		if strings.HasPrefix(base, ".") || base == "node_modules" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// tar uses forward slashes regardless of host OS.
		rel = filepath.ToSlash(rel)

		fi, err := d.Info()
		if err != nil {
			return err
		}
		// Only regular files and directories; skip symlinks/devices/etc. so the
		// bundle can never smuggle a link out of the extraction root on the box.
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if fi.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		count++
		return nil
	})
	if walkErr != nil {
		return count, walkErr
	}
	if err := tw.Close(); err != nil {
		return count, err
	}
	if err := gz.Close(); err != nil {
		return count, err
	}
	if count == 0 {
		return 0, fmt.Errorf("no deployable files found (dotfiles and node_modules are ignored)")
	}
	return count, nil
}

// ---- list -----------------------------------------------------------------

type siteInfo struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Domains   []string `json:"domains"`
	UpdatedTS int64    `json:"updated_ts"`
	Bytes     int64    `json:"bytes"`
}

type siteList struct {
	Sites []siteInfo `json:"sites"`
}

func webList(args []string) error {
	if len(args) != 0 {
		return usagef("usage: vulos web list")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var out siteList
	if err := c.getJSON("/api/web/sites", &out); err != nil {
		return err
	}
	if len(out.Sites) == 0 {
		fmt.Println("no sites deployed")
		return nil
	}
	fmt.Printf("%-20s %-8s %-19s %s\n", "SITE", "SIZE", "UPDATED", "URL")
	for _, s := range out.Sites {
		fmt.Printf("%-20s %-8s %-19s %s\n",
			s.Name, humanBytes(s.Bytes), formatTS(s.UpdatedTS), s.URL)
		for _, d := range s.Domains {
			fmt.Printf("  domain: %s\n", d)
		}
	}
	return nil
}

// ---- rm -------------------------------------------------------------------

type okResult struct {
	OK bool `json:"ok"`
}

func webRm(args []string) error {
	if len(args) != 1 {
		return usagef("usage: vulos web rm <site>")
	}
	site := args[0]
	if !validSiteName(site) {
		return usagef("invalid site name %q", site)
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var out okResult
	if err := c.deleteJSON("/api/web/sites/"+url.PathEscape(site), &out); err != nil {
		return err
	}
	fmt.Printf("deleted site %q\n", site)
	return nil
}

// ---- domain ---------------------------------------------------------------

func webDomain(args []string) error {
	if len(args) == 0 {
		return usagef("usage: vulos web domain <add|status|rm> <site> <domain>")
	}
	switch args[0] {
	case "add":
		return domainAdd(args[1:])
	case "status":
		return domainStatus(args[1:])
	case "rm", "delete":
		return domainRm(args[1:])
	default:
		return usagef("unknown domain subcommand %q", args[0])
	}
}

// addDomainResult mirrors POST /api/web/sites/{site}/domains.
type addDomainResult struct {
	Status   string   `json:"status"`
	CNAME    string   `json:"cname"`
	ARecords []string `json:"a_records"`
}

type domainStatusResult struct {
	Status string `json:"status"`
}

func domainAdd(args []string) error {
	if len(args) != 2 {
		return usagef("usage: vulos web domain add <site> <domain>")
	}
	site, domain := args[0], args[1]
	if !validSiteName(site) {
		return usagef("invalid site name %q", site)
	}
	c, err := newClient()
	if err != nil {
		return err
	}

	var res addDomainResult
	path := fmt.Sprintf("/api/web/sites/%s/domains", url.PathEscape(site))
	if err := c.postJSON(path, map[string]string{"domain": domain}, &res); err != nil {
		return err
	}

	fmt.Printf("domain %q attached to %q — status: %s\n\n", domain, site, res.Status)
	fmt.Println("Create these DNS records at your registrar:")
	if res.CNAME != "" {
		fmt.Printf("  %s\n", res.CNAME)
	}
	for _, a := range res.ARecords {
		fmt.Printf("  %s A %s\n", domain, a)
	}
	fmt.Println()

	if strings.EqualFold(res.Status, "active") {
		fmt.Println("domain is active")
		return nil
	}

	// Poll until active or timeout. Certificate issuance depends on external DNS
	// propagation and a real ACME challenge, so this can legitimately take a
	// while — we poll, we do not claim success we cannot observe.
	fmt.Println("waiting for DNS + certificate (Ctrl-C to stop; re-check later with `vulos web domain status`)…")
	statusPath := fmt.Sprintf("/api/web/sites/%s/domains/%s",
		url.PathEscape(site), url.PathEscape(domain))
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Second)
		var st domainStatusResult
		if err := c.getJSON(statusPath, &st); err != nil {
			fmt.Printf("  (status check failed: %v)\n", err)
			continue
		}
		fmt.Printf("  status: %s\n", st.Status)
		if strings.EqualFold(st.Status, "active") {
			fmt.Printf("domain %q is active\n", domain)
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for %q to become active; DNS may still be propagating — check again with `vulos web domain status %s %s`", domain, site, domain)
}

func domainStatus(args []string) error {
	if len(args) != 2 {
		return usagef("usage: vulos web domain status <site> <domain>")
	}
	site, domain := args[0], args[1]
	if !validSiteName(site) {
		return usagef("invalid site name %q", site)
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var st domainStatusResult
	path := fmt.Sprintf("/api/web/sites/%s/domains/%s",
		url.PathEscape(site), url.PathEscape(domain))
	if err := c.getJSON(path, &st); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", domain, st.Status)
	return nil
}

func domainRm(args []string) error {
	if len(args) != 2 {
		return usagef("usage: vulos web domain rm <site> <domain>")
	}
	site, domain := args[0], args[1]
	if !validSiteName(site) {
		return usagef("invalid site name %q", site)
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var out okResult
	path := fmt.Sprintf("/api/web/sites/%s/domains/%s",
		url.PathEscape(site), url.PathEscape(domain))
	if err := c.deleteJSON(path, &out); err != nil {
		return err
	}
	fmt.Printf("detached %q from %q\n", domain, site)
	return nil
}

// ---- helpers --------------------------------------------------------------

// deriveSiteName turns a directory path into a default site name: the cleaned
// base name, lowercased, with unsafe characters collapsed to dashes.
func deriveSiteName(dir string) string {
	base := filepath.Base(filepath.Clean(dir))
	if base == "." || base == string(filepath.Separator) || base == "" {
		if abs, err := filepath.Abs(dir); err == nil {
			base = filepath.Base(abs)
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// validSiteName mirrors what the box will accept as a DNS label: 1–63 chars of
// [a-z0-9-], not starting or ending with a dash. Client-side validation keeps us
// from packaging a large bundle only to be 400'd.
func validSiteName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func formatTS(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}
