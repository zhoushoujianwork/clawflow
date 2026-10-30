//go:build legacyweb

package clawflow

import "embed"

//go:embed all:web/dist
var EmbeddedDashboard embed.FS

const DashboardRoot = "web/dist"
