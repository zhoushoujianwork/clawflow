//go:build !legacyweb

package clawflow

import "embed"

// Default builds need no Node toolchain. The legacy server retains a migration page.
//go:embed web-retired
var EmbeddedDashboard embed.FS

const DashboardRoot = "web-retired"
