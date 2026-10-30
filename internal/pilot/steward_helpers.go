// Package pilot runs the global platform steward on explicit conversation requests.
package pilot

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func classifyWakeStatus(err error, output string) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, operator.ErrCostLimit) || operator.IsCostLimitError(err, output):
		return "cost-limit"
	case errors.Is(err, operator.ErrBudgetExceeded):
		return "budget-capped"
	case operator.IsAuthError(err, output):
		return "auth-error"
	default:
		return "failed"
	}
}

func clientFor(repo string, cfg *config.Config, creds *config.Credentials) (vcs.Client, error) {
	rc, ok := cfg.Repos[repo]
	if !ok {
		return nil, fmt.Errorf("repo %q not in config", repo)
	}
	if devmode.Active() {
		if err := devmode.CheckBaseURL(rc.BaseURL); err != nil {
			return nil, err
		}
	}
	platform := rc.Platform
	if platform == "" {
		platform = "github"
	}
	switch platform {
	case "github":
		return github.New(creds.GHToken, rc.BaseURL), nil
	case "gitlab":
		return gitlab.New(creds.GitLabToken, rc.BaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported platform %q for repo %q", platform, repo)
	}
}

// pilotRecentSummaries adapts the most recent N PilotRunMeta entries
// for the project into the chat package's compact PilotWakeSummary
// rows. Keeps `chat` free of `snapshot` imports.
func pilotRecentSummaries(projectName string, n int) []chat.PilotWakeSummary {
	metas := snapshot.LastPilotRunSummaries(projectName, n)
	out := make([]chat.PilotWakeSummary, 0, len(metas))
	for _, m := range metas {
		out = append(out, chat.PilotWakeSummary{
			StartedAt: m.StartedAt.UTC().Format(time.RFC3339),
			Status:    m.Status,
			Result:    m.Result,
		})
	}
	return out
}

// extractResult pulls the last `PILOT-RESULT: ...` line out of the Pilot's
// stdout. Used purely for logging; effects are applied by ActionExecutor.
func extractResult(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "PILOT-RESULT:") {
			return s
		}
	}
	return ""
}
