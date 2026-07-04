// Package github is a small client for the GitHub pull-request review-thread
// API used by the language server. It shells out to the "gh" CLI and also
// provides an in-memory ClientMock for tests.
package github

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"time"
)

// ghTimeout bounds each gh invocation so a hung gh process (or a stalled
// network call it makes) cannot block the caller indefinitely.
const ghTimeout = 30 * time.Second

type ReviewComment struct {
	DatabaseID int    `json:"database_id"`
	Body       string `json:"body"`
	Author     string `json:"author"`
}

type ReviewThread struct {
	ThreadID   string          `json:"thread_id"`
	Path       string          `json:"path"`
	Line       int             `json:"line"`
	IsResolved bool            `json:"is_resolved"`
	IsOutdated bool            `json:"is_outdated"`
	Comments   []ReviewComment `json:"comments"`
}

// Service is the set of GitHub operations the server depends on. It is
// implemented by Client (real) and ClientMock (test double).
type Service interface {
	FindPR(owner, repo, branch string) (prNumber int, headSHA string, ok bool)
	FetchReviewThreads(owner, repo string, pr int) ([]ReviewThread, error)
	ResolveReviewThread(threadID string) bool
	UnresolveReviewThread(threadID string) bool
	ReplyToReviewComment(owner, repo string, pr, commentID int, body string) bool
	CreateReviewComment(owner, repo string, pr int, commitID, path string, line int, body string) bool
	CreateReviewCommentRange(owner, repo string, pr int, commitID, path string, startLine, endLine int, body string) bool
}

func runGH(args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			log.Printf("gh %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		} else {
			log.Printf("gh command error: %v", err)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runGHInput runs a gh command feeding input on stdin and returns combined
// output. Arguments are passed as a slice so no dynamic string is interpolated
// directly into the command invocation.
func runGHInput(args []string, input string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
