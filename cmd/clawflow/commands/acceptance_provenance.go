package commands

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Fetch into a disposable repository so stale tracking refs, replace refs and
// concurrent development cannot supply the graph used to inherit provenance.
func acceptanceInheritedDevelopment(ctx context.Context, rc config.Repo, repo string, issue int, pr vcs.PR) (acceptance.Identity, error) {
	origin, err := acceptanceOrigin(ctx, rc, repo)
	if err != nil {
		return acceptance.Identity{}, err
	}
	dir, err := os.MkdirTemp("", "clawflow-acceptance-provenance-")
	if err != nil {
		return acceptance.Identity{}, err
	}
	defer os.RemoveAll(dir)
	base := "refs/remotes/origin/" + pr.BaseBranch
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"check-ref-format", "refs/heads/" + pr.BaseBranch},
		{"check-ref-format", "refs/heads/" + pr.HeadBranch},
		{"remote", "add", "origin", origin},
		{"fetch", "--no-tags", "origin", "refs/heads/" + pr.BaseBranch + ":" + base, "refs/heads/" + pr.HeadBranch + ":refs/clawflow/head"},
	} {
		if _, err := acceptanceGit(ctx, dir, args...); err != nil {
			return acceptance.Identity{}, err
		}
	}
	head, err := acceptanceGit(ctx, dir, "rev-parse", "refs/clawflow/head")
	if err != nil || head != pr.HeadSHA {
		return acceptance.Identity{}, errors.New("acceptance provenance fetched PR head changed")
	}
	type edge struct{ from, merge string }
	var chain []edge
	for depth := 0; depth < 5; depth++ {
		parents, err := acceptanceGit(ctx, dir, "--no-replace-objects", "rev-list", "--parents", "-n", "1", head)
		fields := strings.Fields(parents)
		if err != nil || len(fields) != 3 || fields[0] != head || !acceptanceSHA(fields[1]) || !acceptanceSHA(fields[2]) {
			return acceptance.Identity{}, errDevelopmentHeadMissing
		}
		if _, err := acceptanceGit(ctx, dir, "--no-replace-objects", "merge-base", "--is-ancestor", fields[2], base); err != nil {
			return acceptance.Identity{}, errDevelopmentHeadMissing
		}
		chain = append(chain, edge{fields[1], head})
		head = fields[1]
		identity, err := acceptanceDevelopment(repo, issue, head, pr.Number)
		if err == nil {
			for i := len(chain) - 1; i >= 0; i-- {
				runLog.Info("acceptance/provenance_inherited", "repo", repo, "pr", pr.Number, "from", chain[i].from, "merge", chain[i].merge)
			}
			return identity, nil
		}
		if !errors.Is(err, errDevelopmentHeadMissing) {
			return acceptance.Identity{}, err
		}
	}
	return acceptance.Identity{}, errDevelopmentHeadMissing
}
