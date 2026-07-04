package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/ricoberger/prlsp/internal/github"
	"github.com/ricoberger/prlsp/internal/lsp"
)

// source is the diagnostic source tag shared with the Neovim client.
const source = "github-review"

func uriToPath(uri string) string {
	if strings.HasPrefix(uri, "file://") {
		u, err := url.Parse(uri)
		if err != nil {
			return uri[7:]
		}
		return u.Path
	}
	return uri
}

// uriToRelpath converts a document URI to a repo-relative path. It reads
// s.gitInfo and must be called with s.mu held.
func (s *Server) uriToRelpath(uri string) string {
	path := uriToPath(uri)
	if s.gitInfo == nil || s.gitInfo.Root == "" {
		return path
	}
	root := s.gitInfo.Root
	// Only strip the root when it matches on a path-component boundary, so a
	// sibling directory such as "/repo-backup" is not mistaken for "/repo".
	if path == root {
		return ""
	}
	if strings.HasPrefix(path, root+"/") {
		return path[len(root)+1:]
	}
	return path
}

func makeThreadMessage(t *github.ReviewThread) string {
	parts := make([]string, 0, len(t.Comments))
	for _, c := range t.Comments {
		parts = append(parts, fmt.Sprintf("@%s: %s", c.Author, c.Body))
	}
	return strings.Join(parts, "\n")
}

func makeDiagnostic(t *github.ReviewThread) lsp.Diagnostic {
	line := max(t.Line-1, 0) // LSP is 0-indexed

	// Structured comments let the client render a rich thread view without
	// re-parsing the diagnostic message, which is just the raw comment bodies
	// joined with newlines.
	comments := make([]map[string]any, 0, len(t.Comments))
	for _, c := range t.Comments {
		comments = append(comments, map[string]any{
			"author": c.Author,
			"body":   c.Body,
		})
	}

	dataMap := map[string]any{
		"thread_id":  t.ThreadID,
		"comment_id": 0,
		"path":       t.Path,
		"line":       t.Line,
		"resolved":   t.IsResolved,
		"outdated":   t.IsOutdated,
		"comments":   comments,
	}
	if len(t.Comments) > 0 {
		dataMap["comment_id"] = t.Comments[0].DatabaseID
	}
	rawData, _ := json.Marshal(dataMap)
	raw := json.RawMessage(rawData)

	severity := lsp.SeverityError
	if t.IsResolved {
		severity = lsp.SeverityInformation
	} else if t.IsOutdated {
		severity = lsp.SeverityWarning
	}

	return lsp.Diagnostic{
		Range: lsp.Range{
			Start: lsp.Position{Line: line, Character: 0},
			// A large end character spans the whole line without having to
			// measure its actual length.
			End: lsp.Position{Line: line, Character: 1000},
		},
		Message:  makeThreadMessage(t),
		Severity: severity,
		Source:   source,
		Data:     &raw,
	}
}

// publishFileDiagnostics sends the diagnostics for a single document. It reads
// s.threads and s.gitInfo and must be called with s.mu held.
func (s *Server) publishFileDiagnostics(uri string) {
	rel := s.uriToRelpath(uri)
	var diags []lsp.Diagnostic
	for i := range s.threads {
		t := &s.threads[i]
		if t.Path == rel {
			diags = append(diags, makeDiagnostic(t))
		}
	}
	if diags == nil {
		diags = []lsp.Diagnostic{}
	}
	s.conn.SendNotification("textDocument/publishDiagnostics", lsp.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diags,
	})
}

// refreshThreads re-fetches the review threads for the current PR. It reads and
// writes shared state and must be called with s.mu held. A failed fetch keeps
// the previously loaded threads rather than clearing them.
func (s *Server) refreshThreads() {
	if s.gitInfo == nil || s.prNumber == 0 {
		return
	}
	threads, err := s.gh.FetchReviewThreads(s.gitInfo.Owner, s.gitInfo.Repo, s.prNumber)
	if err != nil {
		// Keep the previously fetched threads so a transient gh failure does
		// not wipe the diagnostics already shown to the user.
		log.Printf("failed to fetch review threads, keeping %d cached: %v", len(s.threads), err)
		return
	}
	s.threads = threads
	unresolved := 0
	for _, t := range s.threads {
		if !t.IsResolved {
			unresolved++
		}
	}
	log.Printf("fetched %d review threads (%d unresolved)", len(s.threads), unresolved)
}

func (s *Server) showMessage(msgType int, message string) {
	s.conn.SendNotification("window/showMessage", lsp.ShowMessageParams{
		Type:    msgType,
		Message: message,
	})
}
