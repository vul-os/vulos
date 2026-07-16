package customdomain

import "embed"

//go:embed migrations
var migrationsFS embed.FS
