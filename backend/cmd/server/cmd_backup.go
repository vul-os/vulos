package main

// CLI subcommands `vulos backup` and `vulos restore` — runnable entrypoints
// that wire the services/sync Compactor (backup → S3) and Restorer
// (restore-from-S3 → live DB) to the real encrypted cluster S3 client.
//
// Usage:
//
//	vulos backup                 — snapshot the local DB and upload to S3
//	vulos restore --confirm      — download the latest snapshot and REPLACE the
//	                               local DB (DESTRUCTIVE: requires --confirm)
//
// Both read the same S3 credentials + cluster passphrase the server uses
// (VULOS_S3_* + VULOS_CLUSTER_PASSPHRASE). They are pure-Go (CGO_ENABLED=0):
// the snapshot uses VACUUM INTO and the restore swaps a validated image in.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"vulos/backend/services/cluster"
	"vulos/backend/services/lease"
	"vulos/backend/services/sync"
)

// backupRestoreEnv groups the resolved configuration shared by both subcommands.
type backupRestoreEnv struct {
	s3Cfg      cluster.S3Config
	passphrase string
	nodeID     string
	dbPath     string
}

// defaultDBPath returns the canonical durable DB the OS uses (auth.db lives at
// ~/.vulos/db/auth.db). Overridable via VULOS_BACKUP_DB.
func defaultDBPath() string {
	if p := os.Getenv("VULOS_BACKUP_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vulos", "db", "auth.db")
}

// resolveBackupEnv loads S3 + passphrase + node id + DB path from the
// environment, returning an error when the cluster is not configured.
func resolveBackupEnv() (backupRestoreEnv, error) {
	cfg := cluster.LoadS3Config()
	pass := os.Getenv("VULOS_CLUSTER_PASSPHRASE")
	if !cfg.Configured() {
		return backupRestoreEnv{}, fmt.Errorf("S3 not configured (set VULOS_S3_ACCESS_KEY / VULOS_S3_SECRET_KEY / VULOS_S3_BUCKET)")
	}
	if pass == "" {
		return backupRestoreEnv{}, fmt.Errorf("VULOS_CLUSTER_PASSPHRASE is required for backup/restore")
	}
	nodeID := os.Getenv("VULOS_NODE_ID")
	if nodeID == "" {
		if h, err := os.Hostname(); err == nil {
			nodeID = h
		}
	}
	return backupRestoreEnv{
		s3Cfg:      cfg,
		passphrase: pass,
		nodeID:     nodeID,
		dbPath:     defaultDBPath(),
	}, nil
}

// leaseCfgFromS3 mirrors cluster.S3Config into the lease package's config shape.
func leaseCfgFromS3(c cluster.S3Config) lease.S3Config {
	return lease.S3Config{
		Endpoint:  c.Endpoint,
		Bucket:    c.Bucket,
		AccessKey: c.AccessKey,
		SecretKey: c.SecretKey,
		UseSSL:    c.UseSSL,
	}
}

// runBackupCLI snapshots the local DB and uploads it to S3 via the Compactor.
// Returns a process exit code.
func runBackupCLI(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dbFlag := fs.String("db", "", "path to the SQLite DB to back up (default: VULOS_BACKUP_DB or ~/.vulos/db/auth.db)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	env, err := resolveBackupEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	if *dbFlag != "" {
		env.dbPath = *dbFlag
	}

	client, err := cluster.NewClient(ctx, env.s3Cfg, env.passphrase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: connect S3: %v\n", err)
		return 1
	}

	compactor, err := sync.BuildCompactor(
		sync.BackupConfig{NodeID: env.nodeID, DBPath: env.dbPath},
		client,
		leaseCfgFromS3(env.s3Cfg),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: build compactor: %v\n", err)
		return 1
	}

	if err := compactor.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	fmt.Printf("backup: snapshot of %s uploaded to bucket %s\n", env.dbPath, env.s3Cfg.Bucket)
	return 0
}

// runRestoreCLI downloads the latest snapshot from S3 and REPLACES the local DB.
// Restore is destructive, so it requires an explicit --confirm flag.
// Returns a process exit code.
func runRestoreCLI(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	confirm := fs.Bool("confirm", false, "REQUIRED: confirm the destructive restore (replaces the local DB)")
	dbFlag := fs.String("db", "", "path to the SQLite DB to restore into (default: VULOS_BACKUP_DB or ~/.vulos/db/auth.db)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*confirm {
		fmt.Fprintln(os.Stderr, "restore: refusing to run without --confirm (restore REPLACES the local DB and is destructive)")
		return 1
	}

	env, err := resolveBackupEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore: %v\n", err)
		return 1
	}
	if *dbFlag != "" {
		env.dbPath = *dbFlag
	}

	client, err := cluster.NewClient(ctx, env.s3Cfg, env.passphrase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore: connect S3: %v\n", err)
		return 1
	}

	restorer, err := sync.BuildRestorer(client, env.dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore: build restorer: %v\n", err)
		return 1
	}

	res, err := restorer.Restore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "restore: %v\n", err)
		return 1
	}
	fmt.Printf("restore: applied snapshot version=%d (%d bytes) → %s\n", res.Version, res.Bytes, env.dbPath)
	return 0
}

// dispatchSubcommand intercepts `backup`/`restore`/`migrate` before the normal
// server boot. It returns (handled, exitCode): when handled is true the caller
// should exit with exitCode; otherwise the server starts normally.
func dispatchSubcommand(ctx context.Context) (handled bool, exitCode int) {
	if len(os.Args) < 2 {
		return false, 0
	}
	switch os.Args[1] {
	case "backup":
		return true, runBackupCLI(ctx, os.Args[2:])
	case "restore":
		return true, runRestoreCLI(ctx, os.Args[2:])
	case "migrate":
		return true, runMigrateCLI(os.Args[2:])
	default:
		return false, 0
	}
}
