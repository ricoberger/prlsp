// Package git detects the local repository context (root, branch) and the
// GitHub owner/repo of the "origin" remote.
package git

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
)

// Info describes the git context of a workspace.
type Info struct {
	Root   string
	Branch string
	Owner  string
	Repo   string
}

func runGit(args []string, cwd string) string {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var (
	sshRemoteRe   = regexp.MustCompile(`^git@github\.com:([^/]+)/([^/]+?)(?:\.git)?$`)
	httpsRemoteRe = regexp.MustCompile(`^https://(?:[^.@/]+@)?github\.com/([^/]+)/([^/]+?)(?:\.git)?$`)
)

func parseRemote(url string) (owner, repo string, ok bool) {
	if m := sshRemoteRe.FindStringSubmatch(url); m != nil {
		return m[1], m[2], true
	}
	if m := httpsRemoteRe.FindStringSubmatch(url); m != nil {
		return m[1], m[2], true
	}
	return "", "", false
}

// Detect inspects the git repository containing workspacePath and returns its
// Info, or nil if it is not a git repo or has no GitHub "origin" remote.
func Detect(workspacePath string) *Info {
	root := runGit([]string{"rev-parse", "--show-toplevel"}, workspacePath)
	if root == "" {
		return nil
	}
	branch := runGit([]string{"rev-parse", "--abbrev-ref", "HEAD"}, workspacePath)
	if branch == "" {
		return nil
	}
	remoteURL := runGit([]string{"remote", "get-url", "origin"}, workspacePath)
	if remoteURL == "" {
		return nil
	}
	owner, repo, ok := parseRemote(remoteURL)
	if !ok {
		return nil
	}
	return &Info{Root: root, Branch: branch, Owner: owner, Repo: repo}
}
