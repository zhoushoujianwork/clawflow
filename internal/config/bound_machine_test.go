package config_test

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestLegacyBindingDoesNotFilterSchedulers(t *testing.T) {
	for _, coordinator := range []bool{false, true} {
		for _, require := range []bool{false, true} {
			settings := config.Settings{RequireBinding: require, Coordinator: config.CoordinatorSettings{Enabled: coordinator}}
			for _, bound := range []string{"", "machine-a", "machine-b"} {
				for _, host := range []string{"", "machine-a", "machine-b"} {
					if !settings.RunsOnHost(config.Repo{BoundMachine: bound}, host) {
						t.Fatalf("legacy binding filtered scheduler: coordinator=%v require=%v bound=%q host=%q", coordinator, require, bound, host)
					}
				}
			}
		}
	}
}
