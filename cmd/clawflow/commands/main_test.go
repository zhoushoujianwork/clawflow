package commands

import (
	"os"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestMain(m *testing.M) {
	issueAdmissionFn = func(_ vcs.Client, _ string, _ config.Repo, issue vcs.Issue) (*issueAdmission, string) {
		return &issueAdmission{Author: issue.Author, Basis: "test", CheckedAt: time.Now().UTC()}, ""
	}
	os.Exit(m.Run())
}
