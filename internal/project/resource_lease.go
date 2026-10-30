package project

import (
	"os"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/resourcelease"
)

// AcquireResources uses the same primary-project mapping as the testing SOP.
// Every repository sharing a host must declare the same key on that machine.
func AcquireResources(repo string, holder resourcelease.Holder) (func(), error) {
	p, err := FindProjectByRepo(repo)
	if err != nil {
		return nil, err
	}
	if p == nil || len(p.SharedResourceKeys) == 0 {
		return func() {}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	holder.Repository = repo
	return resourcelease.Acquire(filepath.Join(home, ".clawflow", "locks", "resources"), p.SharedResourceKeys, holder)
}
