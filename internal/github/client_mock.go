package github

import (
	"encoding/json"
	"fmt"
	"os"
)

// ClientMock is an in-memory Service used by tests. Threads is exported so
// callers can seed and inspect state.
type ClientMock struct {
	Threads []ReviewThread
	nextID  int
}

// NewClientMock loads mock review threads from a JSON fixture of the form
// {"threads": [ ... ]}.
func NewClientMock(fixturePath string) (*ClientMock, error) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return nil, fmt.Errorf("read fixture: %w", err)
	}
	var fixture struct {
		Threads []ReviewThread `json:"threads"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		return nil, fmt.Errorf("parse fixture: %w", err)
	}
	return &ClientMock{Threads: fixture.Threads}, nil
}

func (c *ClientMock) FindPR(owner, repo, branch string) (int, string, bool) {
	return 1, "mock_sha", true
}

func (c *ClientMock) FetchReviewThreads(owner, repo string, pr int) []ReviewThread {
	return c.Threads
}

func (c *ClientMock) ResolveReviewThread(threadID string) bool {
	for i := range c.Threads {
		if c.Threads[i].ThreadID == threadID {
			c.Threads[i].IsResolved = true
			return true
		}
	}
	return false
}

func (c *ClientMock) UnresolveReviewThread(threadID string) bool {
	for i := range c.Threads {
		if c.Threads[i].ThreadID == threadID {
			c.Threads[i].IsResolved = false
			return true
		}
	}
	return false
}

func (c *ClientMock) ReplyToReviewComment(owner, repo string, pr, commentID int, body string) bool {
	for i := range c.Threads {
		for _, comment := range c.Threads[i].Comments {
			if comment.DatabaseID == commentID {
				c.Threads[i].Comments = append(c.Threads[i].Comments, ReviewComment{
					DatabaseID: commentID + 1000,
					Body:       body,
					Author:     "you",
				})
				return true
			}
		}
	}
	return false
}

func (c *ClientMock) CreateReviewComment(owner, repo string, pr int, commitID, path string, line int, body string) bool {
	c.nextID++
	id := c.nextID
	c.Threads = append(c.Threads, ReviewThread{
		ThreadID:   fmt.Sprintf("PRRT_mock_%d", id),
		Path:       path,
		Line:       line,
		IsResolved: false,
		Comments:   []ReviewComment{{DatabaseID: 9000 + id, Body: body, Author: "you"}},
	})
	return true
}

func (c *ClientMock) CreateReviewCommentRange(owner, repo string, pr int, commitID, path string, startLine, endLine int, body string) bool {
	line := max(startLine, endLine)
	return c.CreateReviewComment(owner, repo, pr, commitID, path, line, body)
}
