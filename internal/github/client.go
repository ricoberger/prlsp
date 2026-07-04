package github

import (
	"encoding/json"
	"fmt"
	"log"
)

// Client is the real GitHub Service, backed by the "gh" CLI.
type Client struct{}

const threadsQuery = `
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          line
          originalLine
          path
          comments(first: 50) {
            nodes {
              databaseId
              body
              author { login }
            }
          }
        }
      }
    }
  }
}
`

func (c *Client) FindPR(owner, repo, branch string) (int, string, bool) {
	out, err := runGH([]string{
		"pr", "list",
		"--repo", fmt.Sprintf("%s/%s", owner, repo),
		"--head", branch,
		"--json", "number,headRefOid",
		"--limit", "1",
	})
	if err != nil || out == "" {
		return 0, "", false
	}
	var prs []struct {
		Number     int    `json:"number"`
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil || len(prs) == 0 {
		return 0, "", false
	}
	return prs[0].Number, prs[0].HeadRefOid, true
}

func (c *Client) FetchReviewThreads(owner, repo string, pr int) []ReviewThread {
	out, err := runGH([]string{
		"api", "graphql",
		"-f", fmt.Sprintf("query=%s", threadsQuery),
		"-f", fmt.Sprintf("owner=%s", owner),
		"-f", fmt.Sprintf("repo=%s", repo),
		"-F", fmt.Sprintf("pr=%d", pr),
	})
	if err != nil {
		return nil
	}
	return parseReviewThreads(out)
}

// parseReviewThreads decodes the GraphQL reviewThreads response.
//
// A thread whose "line" is null is "outdated" (its diff hunk no longer maps to
// the current file). Instead of dropping such threads, we anchor them to their
// "originalLine" (an approximate position, since the code has since moved) and
// mark them outdated, so they stay visible. The server then renders outdated
// unresolved threads as warnings and resolved threads as info. A thread with
// neither "line" nor "originalLine" has no anchor and is skipped.
func parseReviewThreads(out string) []ReviewThread {
	var data struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID           string `json:"id"`
							IsResolved   bool   `json:"isResolved"`
							Line         *int   `json:"line"`
							OriginalLine *int   `json:"originalLine"`
							Path         string `json:"path"`
							Comments     struct {
								Nodes []struct {
									DatabaseID int    `json:"databaseId"`
									Body       string `json:"body"`
									Author     *struct {
										Login string `json:"login"`
									} `json:"author"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		log.Printf("failed to parse review threads response: %v", err)
		return nil
	}

	var threads []ReviewThread
	for _, node := range data.Data.Repository.PullRequest.ReviewThreads.Nodes {
		outdated := node.Line == nil
		line := node.Line
		if line == nil {
			// Fall back to the original line for outdated threads.
			line = node.OriginalLine
		}
		if line == nil {
			continue
		}
		var comments []ReviewComment
		for _, c := range node.Comments.Nodes {
			author := "ghost"
			if c.Author != nil {
				author = c.Author.Login
			}
			comments = append(comments, ReviewComment{
				DatabaseID: c.DatabaseID,
				Body:       c.Body,
				Author:     author,
			})
		}
		threads = append(threads, ReviewThread{
			ThreadID:   node.ID,
			Path:       node.Path,
			Line:       *line,
			IsResolved: node.IsResolved,
			IsOutdated: outdated,
			Comments:   comments,
		})
	}
	return threads
}

func (c *Client) ResolveReviewThread(threadID string) bool {
	mutation := `
	mutation($threadId: ID!) {
	  resolveReviewThread(input: {threadId: $threadId}) {
	    thread { isResolved }
	  }
	}
	`
	_, err := runGH([]string{
		"api", "graphql",
		"-f", fmt.Sprintf("query=%s", mutation),
		"-f", fmt.Sprintf("threadId=%s", threadID),
	})
	return err == nil
}

func (c *Client) UnresolveReviewThread(threadID string) bool {
	mutation := `
	mutation($threadId: ID!) {
	  unresolveReviewThread(input: {threadId: $threadId}) {
	    thread { isResolved }
	  }
	}
	`
	_, err := runGH([]string{
		"api", "graphql",
		"-f", fmt.Sprintf("query=%s", mutation),
		"-f", fmt.Sprintf("threadId=%s", threadID),
	})
	return err == nil
}

func (c *Client) ReplyToReviewComment(owner, repo string, pr, commentID int, body string) bool {
	_, err := runGH([]string{
		"api",
		fmt.Sprintf("repos/%s/%s/pulls/%d/comments/%d/replies", owner, repo, pr, commentID),
		"-f", fmt.Sprintf("body=%s", body),
	})
	return err == nil
}

func (c *Client) CreateReviewComment(owner, repo string, pr int, commitID, path string, line int, body string) bool {
	payload, _ := json.Marshal(map[string]any{
		"commit_id": commitID,
		"body":      "",
		"event":     "COMMENT",
		"comments": []map[string]any{
			{
				"path": path,
				"line": line,
				"side": "RIGHT",
				"body": body,
			},
		},
	})
	cmdArgs := []string{
		"api",
		fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, pr),
		"--input", "-",
	}
	out, err := runGHInput(cmdArgs, string(payload))
	if err != nil {
		log.Printf("failed to create review comment: %s", out)
		return false
	}
	return true
}

func (c *Client) CreateReviewCommentRange(owner, repo string, pr int, commitID, path string, startLine, endLine int, body string) bool {
	if startLine <= 0 || endLine <= 0 {
		return false
	}
	if startLine > endLine {
		return false
	}

	payload, _ := json.Marshal(map[string]any{
		"commit_id": commitID,
		"body":      "",
		"event":     "COMMENT",
		"comments": []map[string]any{
			{
				"path":       path,
				"start_line": startLine,
				"line":       endLine,
				"start_side": "RIGHT",
				"side":       "RIGHT",
				"body":       body,
			},
		},
	})
	cmdArgs := []string{
		"api",
		fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, pr),
		"--input", "-",
	}
	out, err := runGHInput(cmdArgs, string(payload))
	if err != nil {
		log.Printf("failed to create multi-line review comment: %s", out)
		return false
	}

	return true
}
