package git

import (
	"testing"
)

func TestParseRemote(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		owner, repo string
		ok          bool
	}{
		{"ssh", "git@github.com:octo/repo.git", "octo", "repo", true},
		{"ssh no suffix", "git@github.com:octo/repo", "octo", "repo", true},
		{"https", "https://github.com/octo/repo.git", "octo", "repo", true},
		{"https no suffix", "https://github.com/octo/repo", "octo", "repo", true},
		{"https with token", "https://x-access-token@github.com/octo/repo.git", "octo", "repo", true},
		{"hyphenated", "git@github.com:my-org/my-repo.git", "my-org", "my-repo", true},
		{"non-github", "git@gitlab.com:octo/repo.git", "", "", false},
		{"garbage", "not a url", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			owner, repo, ok := parseRemote(c.url)
			if ok != c.ok || owner != c.owner || repo != c.repo {
				t.Fatalf("parseRemote(%q) = (%q, %q, %v), want (%q, %q, %v)",
					c.url, owner, repo, ok, c.owner, c.repo, c.ok)
			}
		})
	}
}
