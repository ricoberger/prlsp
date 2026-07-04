package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ricoberger/prlsp/internal/lsp"
)

func (s *Server) handleCodeAction(id *json.RawMessage, params json.RawMessage) {
	var p lsp.CodeActionParams
	json.Unmarshal(params, &p)

	var actions []lsp.CodeAction
	uri := p.TextDocument.URI

	// Diagnostic-tied actions.
	for _, diag := range p.Context.Diagnostics {
		if diag.Source != source || diag.Data == nil {
			continue
		}
		data := extractDiagData(diag.Data)
		threadID, _ := data["thread_id"].(string)
		if threadID == "" {
			continue
		}

		resolved := false
		var preview string
		var author string
		var line int
		for i := range s.threads {
			t := &s.threads[i]
			if t.ThreadID == threadID {
				resolved = t.IsResolved
				line = t.Line
				if len(t.Comments) > 0 {
					author = t.Comments[0].Author
					preview = previewBody(t.Comments[0].Body)
				}
				break
			}
		}

		// Offer the inverse action depending on the thread's state: resolve an
		// unresolved thread, or unresolve a resolved one.
		verb := "Resolve"
		command := "prlsp.resolveReviewThread"
		if resolved {
			verb = "Unresolve"
			command = "prlsp.unresolveReviewThread"
		}
		label := verb + " review thread"
		if author != "" {
			label = fmt.Sprintf("%s @%s L%d: %q", verb, author, line, preview)
		}
		actions = append(actions, lsp.CodeAction{
			Title:       label,
			Kind:        lsp.CodeActionQuickFix,
			Diagnostics: []lsp.Diagnostic{diag},
			Command: &lsp.Command{
				Title:     label,
				Command:   command,
				Arguments: []any{threadID, uri},
			},
		})

		// Open in browser.
		commentIDFloat, _ := data["comment_id"].(float64)
		commentID := int(commentIDFloat)
		if commentID != 0 && s.gitInfo != nil && s.prNumber != 0 {
			info := s.gitInfo
			ghURL := fmt.Sprintf("https://github.com/%s/%s/pull/%d#discussion_r%d",
				info.Owner, info.Repo, s.prNumber, commentID)
			actions = append(actions, lsp.CodeAction{
				Title:       "Open review thread in browser",
				Kind:        lsp.CodeActionEmpty,
				Diagnostics: []lsp.Diagnostic{diag},
				Command: &lsp.Command{
					Title:     "Open review thread in browser",
					Command:   "prlsp.openReviewThreadInBrowser",
					Arguments: []any{ghURL},
				},
			})
		}
	}

	// Selection-tied actions.
	content := s.docs[uri]
	selected := extractSelection(content, p.Range)
	if selected != "" {
		rel := s.uriToRelpath(uri)

		// Reply to existing threads on this file (resolved threads included).
		for i := range s.threads {
			t := &s.threads[i]
			if t.Path != rel || len(t.Comments) == 0 {
				continue
			}
			first := t.Comments[0]
			title := fmt.Sprintf("Reply to @%s L%d: %q", first.Author, t.Line, previewBody(first.Body))
			actions = append(actions, lsp.CodeAction{
				Title: title,
				Kind:  lsp.CodeActionQuickFix,
				Command: &lsp.Command{
					Title:     title,
					Command:   "prlsp.replyToReviewThread",
					Arguments: []any{first.DatabaseID, uri, selected},
				},
			})
		}

		// Create new review comment.
		if s.gitInfo != nil && s.prNumber != 0 && s.headSHA != "" {
			targetLine := p.Range.Start.Line + 1 // 1-indexed for GitHub
			title := fmt.Sprintf("New review comment on L%d", targetLine)
			actions = append(actions, lsp.CodeAction{
				Title: title,
				Kind:  lsp.CodeActionQuickFix,
				Command: &lsp.Command{
					Title:     title,
					Command:   "prlsp.createReviewComment",
					Arguments: []any{uri, targetLine, selected},
				},
			})
		}
	}

	if actions == nil {
		actions = []lsp.CodeAction{}
	}
	s.conn.SendResponse(id, actions)
}

// commentPreviewLen is the maximum number of runes shown from a comment body in
// a code-action title.
const commentPreviewLen = 50

// previewBody returns a single-line, length-limited preview of a comment body
// for use in code-action titles. Newlines are collapsed to spaces and an
// ellipsis is appended only when the body is actually truncated; truncation
// happens on rune boundaries so multi-byte characters are never split.
func previewBody(body string) string {
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.TrimSpace(body)
	r := []rune(body)
	if len(r) > commentPreviewLen {
		return strings.TrimSpace(string(r[:commentPreviewLen])) + "..."
	}
	return body
}
