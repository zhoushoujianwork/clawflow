package commands

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// Build metadata is injected by every supported build entry point.
var BuildCommit, BuildChannel, SourceBranch string

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
var describePattern = regexp.MustCompile(`^(.*)-([0-9]+)-g[0-9a-f]+$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

type comparableVersion struct {
	numbers  [3]int
	distance int
	pre      string
}

func parseVersion(s string) (v comparableVersion, ok bool) {
	if m := describePattern.FindStringSubmatch(s); m != nil {
		var err error
		v.distance, err = strconv.Atoi(m[2])
		if err != nil {
			return v, false
		}
		s = m[1]
	}
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	for i := range v.numbers {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false
		}
		v.numbers[i] = n
	}
	v.pre = m[4]
	return v, true
}

// IsNewerVersion fails closed for unknown versions, including legacy SHA builds.
func IsNewerVersion(current, latest string) bool {
	c, cok := parseVersion(current)
	l, lok := parseVersion(latest)
	if !cok || !lok {
		return false
	}
	for i := range c.numbers {
		if c.numbers[i] != l.numbers[i] {
			return l.numbers[i] > c.numbers[i]
		}
	}
	if c.pre != l.pre {
		if c.pre == "" {
			return false
		}
		if l.pre == "" {
			return true
		}
		a, b := strings.Split(c.pre, "."), strings.Split(l.pre, ".")
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] == b[i] {
				continue
			}
			an, ae := strconv.Atoi(a[i])
			bn, be := strconv.Atoi(b[i])
			if ae == nil && be == nil {
				return bn > an
			}
			if ae == nil {
				return true
			}
			if be == nil {
				return false
			}
			return b[i] > a[i]
		}
		return len(b) > len(a)
	}
	return l.distance > c.distance
}

func buildChannel() string {
	if BuildChannel == "release" || BuildChannel == "source" {
		return BuildChannel
	}
	if _, ok := parseVersion(Version); ok && !strings.Contains(Version, "-g") {
		return "release"
	}
	return "source"
}
func trackedBranch() string {
	if SourceBranch != "" {
		return SourceBranch
	}
	return "main"
}
func installedCommit() string {
	if fullSHARE.MatchString(BuildCommit) {
		return BuildCommit
	}
	if commitPattern.MatchString(Version) {
		return Version
	}
	if m := describePattern.FindStringSubmatch(Version); m != nil {
		_, sha, _ := strings.Cut(Version, "-g")
		return sha
	}
	if _, ok := parseVersion(Version); ok {
		return "refs/tags/" + Version
	}
	return ""
}
func checkReleaseReplacement(current, latest, channel string, explicit, allow bool) error {
	if channel == "source" && !explicit {
		return fmt.Errorf("source build requires --release")
	}
	if current != latest && !IsNewerVersion(current, latest) && !allow {
		return fmt.Errorf("refusing downgrade or unknown version %s → %s; use --allow-downgrade explicitly", current, latest)
	}
	return nil
}
func sourceBuildFlags(version, commit, branch string) string {
	const pkg = "github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands."
	return fmt.Sprintf("-s -w -X %sVersion=%s -X %sBuildCommit=%s -X %sBuildChannel=source -X %sSourceBranch=%s", pkg, version, pkg, commit, pkg, pkg, branch)
}
func sourceRepository() (config.Repo, error) {
	cfg, err := config.Load()
	if err == nil {
		if repo, ok := cfg.Repos[githubRepo]; ok && repo.LocalPath != "" {
			repo.BaseBranch = trackedBranch()
			return repo, nil
		}
	}
	record, err := loadInstallRecord()
	if err == nil && record != nil && record.RepoDir != "" {
		return config.Repo{LocalPath: record.RepoDir, BaseBranch: trackedBranch()}, nil
	}
	return config.Repo{}, fmt.Errorf("source upgrade requires a local_path for %s or install record repo_dir", githubRepo)
}

// SourceStatus uses ls-remote first, fetching only when the tracked head differs.
// Unknown and non-ancestor commits never become upgrade candidates.
func (u *selfUpgrader) SourceStatus(ctx context.Context, repo config.Repo) (head string, behind int, err error) {
	path, branch := expandHomeStr(repo.LocalPath), baseBranchOrMain(repo)
	if path == "" || u.Commit == "" {
		return "", 0, fmt.Errorf("installed commit or source repository is unknown")
	}
	out, err := u.Run(ctx, path, "git", "ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", 0, err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || !fullSHARE.MatchString(fields[0]) {
		return "", 0, fmt.Errorf("unknown branch head")
	}
	head = fields[0]
	if head == u.Commit {
		return head, 0, nil
	}
	if _, err = u.Run(ctx, path, "git", "fetch", "--no-write-fetch-head", "origin", head); err != nil {
		return head, 0, err
	}
	behind, err = u.sourceDistance(ctx, path, head)
	return
}
func (u *selfUpgrader) sourceDistance(ctx context.Context, path, head string) (int, error) {
	if u.Commit == "" {
		return 0, fmt.Errorf("installed commit is unknown")
	}
	if _, err := u.Run(ctx, path, "git", "merge-base", "--is-ancestor", u.Commit, head); err != nil {
		return 0, fmt.Errorf("installed commit is diverged or unknown: %w", err)
	}
	out, err := u.Run(ctx, path, "git", "rev-list", "--count", u.Commit+".."+head)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("unknown ancestry distance")
	}
	return n, nil
}
func currentVersionStatus() api.VersionStatus {
	status := api.VersionStatus{Current: Version, Channel: buildChannel(), Branch: trackedBranch()}
	if status.Channel == "release" {
		status.Latest = FetchLatestTag()
		status.UpdateAvailable = IsNewerVersion(Version, status.Latest)
		status.CanUpgrade = status.UpdateAvailable
		return status
	}
	repo, err := sourceRepository()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		status.Latest, status.Behind, err = newSelfUpgrader().SourceStatus(ctx, repo)
	}
	if err != nil {
		status.Warning = err.Error()
		return status
	}
	status.UpdateAvailable = status.Behind > 0
	status.CanUpgrade = status.UpdateAvailable
	return status
}

// An explicit channel switch still needs ancestry proof unless downgrade was allowed.
func (u *selfUpgrader) verifyReleaseAncestry(ctx context.Context, path, tag string) error {
	if _, ok := parseVersion(tag); !ok {
		return fmt.Errorf("unknown release tag")
	}
	ref := "refs/tags/" + tag
	out, err := u.Run(ctx, expandHomeStr(path), "git", "ls-remote", "--exit-code", "origin", ref, ref+"^{}")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 && len(fields) != 4 {
		return fmt.Errorf("unknown release commit")
	}
	sha := fields[len(fields)-2]
	if !fullSHARE.MatchString(sha) {
		return fmt.Errorf("unknown release commit")
	}
	if _, err := u.Run(ctx, expandHomeStr(path), "git", "fetch", "--no-write-fetch-head", "origin", sha); err != nil {
		return err
	}
	if _, err := u.sourceDistance(ctx, expandHomeStr(path), sha); err != nil {
		return fmt.Errorf("refusing release downgrade or unknown ancestry; --allow-downgrade required: %w", err)
	}
	return nil
}
