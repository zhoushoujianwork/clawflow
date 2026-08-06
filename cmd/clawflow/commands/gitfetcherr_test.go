package commands

import "strings"

import "testing"

func TestClassifyGitFetchError(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   gitFetchErrKind
	}{
		{
			// The exact issue #300 signature: base_branch was "origin".
			name:   "misconfigured base branch",
			output: "fatal: couldn't find remote ref origin\n",
			want:   gitFetchRefNotFound,
		},
		{
			name:   "unknown revision",
			output: "fatal: ambiguous argument 'origin/nope': unknown revision or path not in the working tree.",
			want:   gitFetchRefNotFound,
		},
		{
			name:   "http auth rejected",
			output: "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/o/r/'",
			want:   gitFetchAuth,
		},
		{
			name:   "ssh key rejected",
			output: "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
			want:   gitFetchAuth,
		},
		{
			name:   "dns failure",
			output: "ssh: Could not resolve hostname github.com: nodename nor servname provided",
			want:   gitFetchNetwork,
		},
		{
			name:   "connect timeout",
			output: "ssh: connect to host github.com port 22: Connection timed out",
			want:   gitFetchNetwork,
		},
		{
			name:   "empty output",
			output: "",
			want:   gitFetchUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyGitFetchError(tc.output); got != tc.want {
				t.Errorf("classifyGitFetchError(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestDescribeGitFetchFailure_RefNotFoundNamesConfig(t *testing.T) {
	err := describeGitFetchFailure("origin", "/tmp/clone",
		"fatal: couldn't find remote ref origin\n", "main", errTestExit128)
	msg := err.Error()

	for _, want := range []string{"base_branch", "misconfigured", `"main"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
	// The regression this test guards: issue #300's blanket network wording.
	if strings.Contains(msg, "without network access") {
		t.Errorf("ref-not-found must not be reported as a network problem: %s", msg)
	}
}

func TestDescribeGitFetchFailure_NetworkKeepsNetworkWording(t *testing.T) {
	err := describeGitFetchFailure("main", "/tmp/clone",
		"ssh: Could not resolve hostname github.com", "main", errTestExit128)
	if !strings.Contains(err.Error(), "network access") {
		t.Errorf("network failure should say so: %s", err)
	}
}

func TestDescribeGitFetchFailure_AuthNamesCredentials(t *testing.T) {
	err := describeGitFetchFailure("main", "/tmp/clone",
		"fatal: Authentication failed for 'https://github.com/o/r/'", "main", errTestExit128)
	msg := err.Error()
	if !strings.Contains(msg, "credentials") {
		t.Errorf("auth failure should name credentials: %s", msg)
	}
	if strings.Contains(msg, "network access") {
		t.Errorf("auth failure must not be reported as network: %s", msg)
	}
}

var errTestExit128 = errTestExit(128)

type errTestExit int

func (e errTestExit) Error() string { return "exit status 128" }
