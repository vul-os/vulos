// routes_boot.go — boot artifact HTTP routes for the vulos.cloud control-plane.
//
// Routes registered:
//
//	GET /boot/ipxe          — iPXE boot script (text/plain)
//	GET /boot/kernel        — kernel binary (serve file or 302 to bucket)
//	GET /boot/initramfs     — initramfs image (serve file or 302 to bucket)
//	GET /boot/manifest.json — pre-signed OTA manifest (serve file or 302 to bucket)
//
// Trust model: no HTTP-layer auth. The manifest is a pre-signed static artifact
// verified by the initramfs against the embedded OTA public key. The control
// plane NEVER holds or loads the signing key — it only passes the artifact
// through opaquely.
//
// Env vars consumed at registration time (captured in closures):
//
//	BOOT_ARTIFACT_DIR      — local directory containing kernel, initramfs.img,
//	                         manifest.json. If empty, all four endpoints redirect
//	                         to OTA_PUBLIC_BUCKET_BASE.
//	OTA_PUBLIC_BUCKET_BASE — public bucket base URL (no trailing slash).
//	                         Used for 302 redirects when BOOT_ARTIFACT_DIR is
//	                         empty, and for the iPXE script kernel/initramfs URLs.
//
// CORS: Access-Control-Allow-Origin: * is set on all four endpoints. The
// initramfs fetches these over HTTPS from a bare HTTP client; CORS is benign
// but expected by browser-based diagnostics and the Vulos dashboard SPA.
//

package cproutes

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// RegisterBoot wires the four boot artifact endpoints into mux.
// It reads BOOT_ARTIFACT_DIR and OTA_PUBLIC_BUCKET_BASE from the environment
// once at call time and captures the values in handler closures.
func RegisterBoot(mux *http.ServeMux) {
	artifactDir := os.Getenv("BOOT_ARTIFACT_DIR")
	bucketBase := strings.TrimRight(os.Getenv("OTA_PUBLIC_BUCKET_BASE"), "/")

	mux.HandleFunc("GET /boot/ipxe", bootIPXEHandler(artifactDir, bucketBase))
	mux.HandleFunc("GET /boot/kernel", bootFileHandler(artifactDir, bucketBase, "kernel", "kernel"))
	mux.HandleFunc("GET /boot/initramfs", bootFileHandler(artifactDir, bucketBase, "initramfs.img", "initramfs"))
	mux.HandleFunc("GET /boot/manifest.json", bootManifestHandler(artifactDir, bucketBase))
}

// bootCORSHeaders sets the permissive CORS headers required on all boot endpoints.
func bootCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// bootIPXEHandler returns the iPXE boot script handler.
//
// If artifactDir is non-empty the kernel and initramfs URLs point back at this
// server's /boot/kernel and /boot/initramfs paths (host from request Host
// header). If artifactDir is empty the URLs point at the public bucket.
func bootIPXEHandler(artifactDir, bucketBase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bootCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var kernelURL, initramfsURL string
		if artifactDir != "" {
			// Point back at this server using the request's Host.
			scheme := "https"
			if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
				scheme = "http"
			} else if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
				scheme = proto
			}
			host := r.Host
			kernelURL = scheme + "://" + host + "/boot/kernel"
			initramfsURL = scheme + "://" + host + "/boot/initramfs"
		} else {
			if bucketBase == "" {
				http.Error(w, "boot: no BOOT_ARTIFACT_DIR or OTA_PUBLIC_BUCKET_BASE configured", http.StatusNotFound)
				return
			}
			kernelURL = bucketBase + "/kernel"
			initramfsURL = bucketBase + "/initramfs"
		}

		script := "#!ipxe\n" +
			"kernel " + kernelURL + "\n" +
			"initrd " + initramfsURL + "\n" +
			"boot\n"

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(script))
	}
}

// bootFileHandler returns a handler that serves a static file from artifactDir
// or redirects to bucketBase/<bucketName>. fileName is the on-disk name inside
// artifactDir; bucketName is the last path segment used in the redirect URL.
func bootFileHandler(artifactDir, bucketBase, fileName, bucketName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bootCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if artifactDir != "" {
			path := filepath.Join(artifactDir, fileName)
			http.ServeFile(w, r, path)
			return
		}

		if bucketBase == "" {
			http.Error(w, "boot: no BOOT_ARTIFACT_DIR or OTA_PUBLIC_BUCKET_BASE configured", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, bucketBase+"/"+bucketName, http.StatusFound)
	}
}

// bootManifestHandler returns a handler that serves manifest.json opaquely.
//
// Critical invariant: this handler NEVER synthesises a manifest. If neither
// BOOT_ARTIFACT_DIR nor OTA_PUBLIC_BUCKET_BASE is configured it returns 404.
// The manifest is a pre-signed static file — the cp never loads or touches any
// signing key.
func bootManifestHandler(artifactDir, bucketBase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bootCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if artifactDir != "" {
			path := filepath.Join(artifactDir, "manifest.json")
			// Verify the file exists before attempting to serve it. We must never
			// synthesise an unsigned manifest — if the file is absent, 404.
			if _, err := os.Stat(path); os.IsNotExist(err) {
				http.Error(w, "boot: manifest.json not found in BOOT_ARTIFACT_DIR", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			http.ServeFile(w, r, path)
			return
		}

		if bucketBase == "" {
			http.Error(w, "boot: no signed manifest available", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, bucketBase+"/manifest.json", http.StatusFound)
	}
}
