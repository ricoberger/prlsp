package server

import (
	"encoding/json"
	"fmt"

	"github.com/ricoberger/prlsp/internal/lsp"
)

func (s *Server) handleExecuteCommand(id *json.RawMessage, params json.RawMessage) {
	var p lsp.ExecuteCommandParams
	json.Unmarshal(params, &p)

	// The cmd* handlers read and mutate shared session state (threads, gitInfo,
	// docs), so hold the lock for the whole dispatch.
	s.mu.Lock()
	switch p.Command {
	case "prlsp.resolveReviewThread":
		s.cmdResolveReviewThread(p.Arguments)
	case "prlsp.unresolveReviewThread":
		s.cmdUnresolveReviewThread(p.Arguments)
	case "prlsp.openReviewThreadInBrowser":
		s.cmdOpenReviewThreadInBrowser(p.Arguments)
	case "prlsp.createReviewComment":
		s.cmdCreateReviewComment(p.Arguments)
	case "prlsp.createReviewCommentRange":
		s.cmdCreateReviewCommentRange(p.Arguments)
	case "prlsp.replyToReviewThread":
		s.cmdReplyToReviewThread(p.Arguments)
	case "prlsp.refreshReviewThreads":
		s.cmdRefreshReviewThreads()
	}
	s.mu.Unlock()

	// Always respond with null result.
	s.conn.SendResponse(id, nil)
}

func (s *Server) cmdResolveReviewThread(args []json.RawMessage) {
	// Resolving only needs the thread ID, so it does not depend on gitInfo.
	if len(args) < 2 {
		return
	}
	var threadID, uri string
	json.Unmarshal(args[0], &threadID)
	json.Unmarshal(args[1], &uri)

	ok := s.gh.ResolveReviewThread(threadID)
	if ok {
		for i := range s.threads {
			if s.threads[i].ThreadID == threadID {
				s.threads[i].IsResolved = true
			}
		}
		s.publishFileDiagnostics(uri)
		s.showMessage(lsp.MessageTypeInfo, "Review thread resolved")
	} else {
		s.showMessage(lsp.MessageTypeError, "Failed to resolve review thread")
	}
}

func (s *Server) cmdUnresolveReviewThread(args []json.RawMessage) {
	// Unresolving only needs the thread ID, so it does not depend on gitInfo.
	if len(args) < 2 {
		return
	}
	var threadID, uri string
	json.Unmarshal(args[0], &threadID)
	json.Unmarshal(args[1], &uri)

	ok := s.gh.UnresolveReviewThread(threadID)
	if ok {
		for i := range s.threads {
			if s.threads[i].ThreadID == threadID {
				s.threads[i].IsResolved = false
			}
		}
		s.publishFileDiagnostics(uri)
		s.showMessage(lsp.MessageTypeInfo, "Review thread unresolved")
	} else {
		s.showMessage(lsp.MessageTypeError, "Failed to unresolve review thread")
	}
}

func (s *Server) cmdOpenReviewThreadInBrowser(args []json.RawMessage) {
	if len(args) < 1 {
		return
	}
	var ghURL string
	json.Unmarshal(args[0], &ghURL)

	// Sent as a request (window/showDocument has a response), but we don't need
	// the result.
	s.conn.SendRequest("window/showDocument", lsp.ShowDocumentParams{
		URI:      ghURL,
		External: true,
	})
}

func (s *Server) cmdCreateReviewComment(args []json.RawMessage) {
	if len(args) < 3 {
		return
	}
	info := s.gitInfo
	if info == nil || s.prNumber == 0 || s.headSHA == "" {
		return
	}
	var uri string
	var line int
	var body string
	json.Unmarshal(args[0], &uri)
	json.Unmarshal(args[1], &line)
	json.Unmarshal(args[2], &body)

	rel := s.uriToRelpath(uri)
	ok := s.gh.CreateReviewComment(info.Owner, info.Repo, s.prNumber, s.headSHA, rel, line, body)
	if ok {
		s.refreshThreads()
		s.publishFileDiagnostics(uri)
		s.showMessage(lsp.MessageTypeInfo, fmt.Sprintf("Review comment posted on L%d", line))
	} else {
		s.showMessage(lsp.MessageTypeError, "Failed to post review comment (line may not be in PR diff)")
	}
}

func (s *Server) cmdCreateReviewCommentRange(args []json.RawMessage) {
	if len(args) < 4 {
		return
	}
	info := s.gitInfo
	if info == nil || s.prNumber == 0 || s.headSHA == "" {
		return
	}
	var uri string
	var startLine, endLine int
	var body string
	json.Unmarshal(args[0], &uri)
	json.Unmarshal(args[1], &startLine)
	json.Unmarshal(args[2], &endLine)
	json.Unmarshal(args[3], &body)

	rel := s.uriToRelpath(uri)
	ok := s.gh.CreateReviewCommentRange(info.Owner, info.Repo, s.prNumber, s.headSHA, rel, startLine, endLine, body)
	if ok {
		s.refreshThreads()
		s.publishFileDiagnostics(uri)
		s.showMessage(lsp.MessageTypeInfo, fmt.Sprintf("Review comment posted on L%d-L%d", startLine, endLine))
	} else {
		s.showMessage(lsp.MessageTypeError, "Failed to post multi-line review comment (range may not be in PR diff)")
	}
}

func (s *Server) cmdReplyToReviewThread(args []json.RawMessage) {
	if len(args) < 3 {
		return
	}
	info := s.gitInfo
	if info == nil || s.prNumber == 0 {
		return
	}
	var commentID int
	var uri, body string
	json.Unmarshal(args[0], &commentID)
	json.Unmarshal(args[1], &uri)
	json.Unmarshal(args[2], &body)

	ok := s.gh.ReplyToReviewComment(info.Owner, info.Repo, s.prNumber, commentID, body)
	if ok {
		s.refreshThreads()
		s.publishFileDiagnostics(uri)
		s.showMessage(lsp.MessageTypeInfo, "Reply posted")
	} else {
		s.showMessage(lsp.MessageTypeError, "Failed to post reply")
	}
}

func (s *Server) cmdRefreshReviewThreads() {
	s.refreshThreads()
	for uri := range s.docs {
		s.publishFileDiagnostics(uri)
	}
	s.showMessage(lsp.MessageTypeInfo, "Review threads refreshed")
}
