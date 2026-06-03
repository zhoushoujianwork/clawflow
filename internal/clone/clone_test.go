package clone

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestBuildCloneURL(t *testing.T) {
	ghToken := &Token{GHToken: "ght_secret"}
	glToken := &Token{GitLabToken: "glt_secret"}

	cases := []struct {
		name      string
		ownerRepo string
		repoCfg   config.Repo
		token     *Token
		protocol  string
		want      string
	}{
		{
			name:      "ssh default for github even with token",
			ownerRepo: "zhoushoujianwork/motobox",
			repoCfg:   config.Repo{Platform: "github"},
			token:     ghToken,
			protocol:  "ssh",
			want:      "git@github.com:zhoushoujianwork/motobox.git",
		},
		{
			name:      "empty protocol falls back to ssh",
			ownerRepo: "zhoushoujianwork/motobox",
			repoCfg:   config.Repo{Platform: "github"},
			token:     ghToken,
			protocol:  "",
			want:      "git@github.com:zhoushoujianwork/motobox.git",
		},
		{
			name:      "https mode embeds github token",
			ownerRepo: "zhoushoujianwork/motobox",
			repoCfg:   config.Repo{Platform: "github"},
			token:     ghToken,
			protocol:  "https",
			want:      "https://x-access-token:ght_secret@github.com/zhoushoujianwork/motobox.git",
		},
		{
			name:      "https mode without token falls back to ssh",
			ownerRepo: "zhoushoujianwork/motobox",
			repoCfg:   config.Repo{Platform: "github"},
			token:     nil,
			protocol:  "https",
			want:      "git@github.com:zhoushoujianwork/motobox.git",
		},
		{
			name:      "gitlab ssh default strips scheme from base_url",
			ownerRepo: "group/proj",
			repoCfg:   config.Repo{Platform: "gitlab", BaseURL: "https://gitlab.example.com/"},
			token:     glToken,
			protocol:  "ssh",
			want:      "git@gitlab.example.com:group/proj.git",
		},
		{
			name:      "gitlab https mode embeds oauth2 token",
			ownerRepo: "group/proj",
			repoCfg:   config.Repo{Platform: "gitlab", BaseURL: "https://gitlab.example.com"},
			token:     glToken,
			protocol:  "https",
			want:      "https://oauth2:glt_secret@gitlab.example.com/group/proj.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCloneURL(tc.ownerRepo, tc.repoCfg, tc.token, tc.protocol)
			if got != tc.want {
				t.Errorf("buildCloneURL(%q, %+v, token, %q)\n  got:  %s\n  want: %s",
					tc.ownerRepo, tc.repoCfg, tc.protocol, got, tc.want)
			}
		})
	}
}

func TestResolveCloneProtocol(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "ssh"},
		{"ssh", "ssh"},
		{"SSH", "ssh"},
		{"https", "https"},
		{"HTTPS", "https"},
		{"garbage", "ssh"},
	}
	for _, tc := range cases {
		s := &config.Settings{CloneProtocol: tc.in}
		if got := s.ResolveCloneProtocol(); got != tc.want {
			t.Errorf("ResolveCloneProtocol(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
