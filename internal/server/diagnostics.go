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

func (s *Server) uriToRelpath(uri string) string {
	path := uriToPath(uri)
	if s.gitInfo != nil && s.gitInfo.Root != "" && strings.HasPrefix(path, s.gitInfo.Root) {
		return strings.TrimPrefix(path[len(s.gitInfo.Root):], "/")
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
	// re-parsing the (compact, markdown-free) diagnostic message.
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
			End:   lsp.Position{Line: line, Character: 1000},
		},
		Message:  makeThreadMessage(t),
		Severity: severity,
		Source:   source,
		Data:     &raw,
	}
}

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

func (s *Server) refreshThreads() {
	if s.gitInfo == nil || s.prNumber == 0 {
		return
	}
	s.threads = s.gh.FetchReviewThreads(s.gitInfo.Owner, s.gitInfo.Repo, s.prNumber)
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
