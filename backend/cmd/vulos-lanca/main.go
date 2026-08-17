// Command vulos-lanca is the OPERATOR-SIDE tool that runs the name-constrained
// LAN certificate authority described in backend/internal/lanca.
//
// It is deliberately a separate binary from vulos-server. The separation is the
// security control: vulos-server runs on the box, this runs on the operator's
// machine, and the CA private key only ever exists where this runs. Building it
// into the server would put the key on the box and give away the entire
// property the design is built on.
//
// Typical use:
//
//	# once, on the operator's machine
//	vulos-lanca init -label "kitchen shelf"
//	vulos-lanca root > vulos-root.crt        # install this on phones/laptops
//
//	# per box, whenever its advertised names change or the leaf nears expiry
//	vulos-lanca issue -csr box.csr -dns vulos.local,vulos-2.local -ip 192.168.1.50 \
//	    -out lan.crt
//
// The box produces box.csr from the key it ALREADY has, so its SPKI — and every
// native client's pin — is unchanged by re-issuance.
package main

import (
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vulos/backend/internal/lanca"
)

const usage = `vulos-lanca — operator-side LAN certificate authority

  init      create the name-constrained root CA (once)
  root      print the root certificate PEM (this is what you install on devices)
  issue     sign a box's CSR for a set of advertised names
  inspect   print a certificate's SANs and constraints

The CA private key lives ONLY where this command runs. It must never be copied
onto a Vulos box; this tool refuses to write it to a box path.

Run 'vulos-lanca <command> -h' for per-command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "root":
		err = cmdRoot(os.Args[2:])
	case "issue":
		err = cmdIssue(os.Args[2:])
	case "inspect":
		err = cmdInspect(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "vulos-lanca: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "vulos-lanca: %v\n", err)
		os.Exit(1)
	}
}

// defaultDir is where the CA lives on the operator's machine. Under the user's
// home, never under a system path a box might share.
func defaultDir() string {
	if v := strings.TrimSpace(os.Getenv("VULOS_LANCA_DIR")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".vulos-lanca"
	}
	return filepath.Join(home, ".vulos-lanca")
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "directory to hold the CA key and certificate")
	label := fs.String("label", "", "human label folded into the root's Common Name, so an owner with several roots installed can tell them apart on a device's certificate list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := lanca.NewRoot(*label)
	if err != nil {
		return err
	}
	if err := lanca.SaveRoot(*dir, root); err != nil {
		return err
	}

	fmt.Printf("Created a name-constrained LAN root CA.\n\n")
	fmt.Printf("  directory   %s\n", *dir)
	fmt.Printf("  subject     %s\n", root.Cert.Subject.CommonName)
	fmt.Printf("  expires     %s\n", root.Cert.NotAfter.Format(time.RFC3339))
	fmt.Printf("  DNS limit   %s\n", strings.Join(lanca.PermittedDNSDomains, ", "))
	fmt.Printf("  IP limit    %s\n", strings.Join(lanca.SortedPermittedCIDRs(), ", "))
	fmt.Printf("\nThis root can ONLY vouch for those names. If its private key is stolen,\n")
	fmt.Printf("it still cannot mint a working certificate for a public site.\n\n")
	fmt.Printf("The private key is at %s and must stay on this machine.\n",
		filepath.Join(*dir, lanca.RootKeyFile))
	fmt.Printf("Next: `vulos-lanca root` prints the certificate to install on your devices.\n")
	return nil
}

func cmdRoot(args []string) error {
	fs := flag.NewFlagSet("root", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "CA directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Read the certificate directly rather than through OpenRoot: printing the
	// PUBLIC root should not require the private key to be present or readable,
	// so an operator can distribute the root from a copy that has no key at all.
	b, err := os.ReadFile(filepath.Join(*dir, lanca.RootCertFile))
	if err != nil {
		return fmt.Errorf("read root certificate: %w", err)
	}
	_, err = os.Stdout.Write(b)
	return err
}

func cmdIssue(args []string) error {
	fs := flag.NewFlagSet("issue", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "CA directory")
	csrPath := fs.String("csr", "", "path to the box's PEM certificate-signing request (REQUIRED)")
	dnsCSV := fs.String("dns", "", "comma-separated DNS names the box advertises (REQUIRED)")
	ipCSV := fs.String("ip", "", "comma-separated LAN IP addresses the box answers on")
	out := fs.String("out", "", "write the leaf here instead of stdout")
	days := fs.Int("days", 0, "leaf lifetime in days (default 397, one day under the 398-day ceiling Apple and Chrome apply to public chains)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *csrPath == "" {
		return fmt.Errorf("-csr is required; the box generates it from the key it already has, so re-issuance does not change its SPKI or break any native client's pin")
	}
	if strings.TrimSpace(*dnsCSV) == "" {
		return fmt.Errorf("-dns is required and must be the names the box ACTUALLY advertises. " +
			"Do not guess: if avahi renamed a colliding host to vulos-2.local, or the owner set " +
			"VULOS_HOSTNAME, a guessed name produces a certificate the browser rejects for the " +
			"one name the owner actually types")
	}

	// FilterIssuable rather than NewNameSet: an operator can paste the box's
	// WHOLE advertised name list (which legitimately contains bare labels no
	// permittedSubtrees entry can cover) and get a working certificate for the
	// dotted subset. Skipped names are printed loudly rather than swallowed —
	// that is both how the owner learns which names still warn, and how a typo
	// like "vuloss.local" surfaces instead of quietly producing a certificate
	// for a name nobody uses.
	ns, skippedDNS, skippedIPs := lanca.FilterIssuable(splitCSV(*dnsCSV), parseIPs(splitCSV(*ipCSV)))
	if len(ns.DNSNames) == 0 {
		return fmt.Errorf("none of the names in -dns can be issued by this CA (it is name-constrained to %s); nothing to sign",
			strings.Join(lanca.PermittedDNSDomains, ", "))
	}
	for _, n := range skippedDNS {
		fmt.Fprintf(os.Stderr, "SKIPPED %s — outside this CA's permitted subtrees, so it will NOT be on the certificate.\n", n)
	}
	for _, ip := range skippedIPs {
		fmt.Fprintf(os.Stderr, "SKIPPED %s — outside this CA's permitted IP ranges, so it will NOT be on the certificate.\n", ip)
	}
	if len(skippedDNS) > 0 || len(skippedIPs) > 0 {
		fmt.Fprintf(os.Stderr, "Browsing to a skipped name still works, but shows the one-click warning\n")
		fmt.Fprintf(os.Stderr, "rather than a padlock. Check the list above for typos.\n\n")
	}

	root, err := lanca.OpenRoot(*dir)
	if err != nil {
		return err
	}
	csrPEM, err := os.ReadFile(*csrPath)
	if err != nil {
		return fmt.Errorf("read CSR: %w", err)
	}

	var ttl time.Duration
	if *days > 0 {
		ttl = time.Duration(*days) * 24 * time.Hour
	}
	certPEM, leaf, err := root.IssueFromCSR(csrPEM, ns, ttl)
	if err != nil {
		return err
	}

	if *out != "" {
		if err := os.WriteFile(*out, certPEM, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *out, err)
		}
		fmt.Fprintf(os.Stderr, "Issued leaf for %s\n", strings.Join(leaf.DNSNames, ", "))
		fmt.Fprintf(os.Stderr, "  expires  %s\n", leaf.NotAfter.Format(time.RFC3339))
		fmt.Fprintf(os.Stderr, "  SPKI pin %s\n", lanca.SPKISHA256(leaf))
		fmt.Fprintf(os.Stderr, "  written  %s\n", *out)
		fmt.Fprintf(os.Stderr, "\nThe SPKI pin above is the box's OWN key, unchanged by this issuance.\n")
		fmt.Fprintf(os.Stderr, "Native clients that already paired with this box do not need to re-pair.\n")
		return nil
	}
	_, err = os.Stdout.Write(certPEM)
	return err
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: vulos-lanca inspect <certificate.pem>")
	}
	pemBytes, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	cert, err := lanca.ParseCertPEM(pemBytes)
	if err != nil {
		return err
	}
	fmt.Printf("subject        %s\n", cert.Subject.CommonName)
	fmt.Printf("issuer         %s\n", cert.Issuer.CommonName)
	fmt.Printf("not before     %s\n", cert.NotBefore.Format(time.RFC3339))
	fmt.Printf("not after      %s\n", cert.NotAfter.Format(time.RFC3339))
	fmt.Printf("is CA          %v\n", cert.IsCA)
	fmt.Printf("SPKI pin       %s\n", lanca.SPKISHA256(cert))
	if len(cert.DNSNames) > 0 {
		fmt.Printf("DNS SANs       %s\n", strings.Join(cert.DNSNames, ", "))
	}
	if len(cert.IPAddresses) > 0 {
		ips := make([]string, 0, len(cert.IPAddresses))
		for _, ip := range cert.IPAddresses {
			ips = append(ips, ip.String())
		}
		fmt.Printf("IP SANs        %s\n", strings.Join(ips, ", "))
	}
	if cert.IsCA {
		if len(cert.PermittedDNSDomains) == 0 && len(cert.PermittedIPRanges) == 0 {
			fmt.Printf("constraints    NONE — this CA can vouch for ANY name\n")
		} else {
			fmt.Printf("permitted DNS  %s\n", strings.Join(cert.PermittedDNSDomains, ", "))
			ranges := make([]string, 0, len(cert.PermittedIPRanges))
			for _, r := range cert.PermittedIPRanges {
				ranges = append(ranges, r.String())
			}
			fmt.Printf("permitted IP   %s\n", strings.Join(ranges, ", "))
		}
		fmt.Printf("path length    %s\n", pathLenText(cert))
	}
	return nil
}

func pathLenText(cert *x509.Certificate) string {
	if cert.MaxPathLenZero {
		return "0 (cannot sign a subordinate CA)"
	}
	if cert.MaxPathLen > 0 {
		return fmt.Sprintf("%d", cert.MaxPathLen)
	}
	return "unlimited"
}

// parseIPs turns text addresses into net.IPs, dropping anything unparseable —
// NewNameSet then rejects anything outside the CA's permitted ranges, so a typo
// surfaces as a refusal rather than as a certificate missing the address the
// owner actually browses to.
func parseIPs(in []string) []net.IP {
	out := make([]net.IP, 0, len(in))
	for _, s := range in {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
