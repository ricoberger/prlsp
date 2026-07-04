// Package server implements the prlsp language server: it holds the session
// state, runs the JSON-RPC read loop, and handles LSP requests by surfacing
// GitHub pull-request review threads as diagnostics and code actions.
package server

import (
	"encoding/json"
	"io"
	"log"

	"github.com/ricoberger/prlsp/internal/git"
	"github.com/ricoberger/prlsp/internal/github"
	"github.com/ricoberger/prlsp/internal/jsonrpc"
	"github.com/ricoberger/prlsp/internal/lsp"
)

// Server holds the language server's session state.
type Server struct {
	conn     *jsonrpc.Conn
	docs     map[string]string // uri -> full content
	gitInfo  *git.Info
	prNumber int
	headSHA  string
	threads  []github.ReviewThread
	gh       github.Service
	rootURI  string
}

// New creates a Server that communicates over conn and talks to GitHub via gh.
func New(conn *jsonrpc.Conn, gh github.Service) *Server {
	return &Server{
		conn: conn,
		docs: make(map[string]string),
		gh:   gh,
	}
}

// Run reads and dispatches messages until the client disconnects or sends
// "exit".
func (s *Server) Run() {
	for {
		msg, err := s.conn.ReadMessage()
		if err != nil {
			if err == io.EOF {
				log.Println("client disconnected")
				return
			}
			log.Printf("read error: %v", err)
			return
		}

		// An empty method indicates a client response to one of our requests
		// (e.g. window/showDocument); we do not need the result.
		if msg.Method == "" {
			continue
		}

		log.Printf(">> %s", msg.Method)

		switch msg.Method {
		case "initialize":
			s.handleInitialize(msg.ID, msg.Params)
		case "initialized":
			s.handleInitialized()
		case "textDocument/didOpen":
			s.handleDidOpen(msg.Params)
		case "textDocument/didChange":
			s.handleDidChange(msg.Params)
		case "textDocument/codeAction":
			s.handleCodeAction(msg.ID, msg.Params)
		case "workspace/executeCommand":
			s.handleExecuteCommand(msg.ID, msg.Params)
		case "shutdown":
			s.conn.SendResponse(msg.ID, nil)
		case "exit":
			return
		default:
			// Ignore unknown methods; respond with null if it has an ID.
			if msg.ID != nil {
				s.conn.SendResponse(msg.ID, nil)
			}
		}
	}
}

func (s *Server) handleInitialize(id *json.RawMessage, params json.RawMessage) {
	var p lsp.InitializeParams
	json.Unmarshal(params, &p)

	// Save root URI for later.
	if len(p.WorkspaceFolders) > 0 {
		s.rootURI = p.WorkspaceFolders[0].URI
	} else if p.RootURI != "" {
		s.rootURI = p.RootURI
	}

	result := lsp.InitializeResult{
		Capabilities: lsp.ServerCapabilities{
			TextDocumentSync:   1, // Full
			CodeActionProvider: true,
			ExecuteCommandProvider: &lsp.ExecuteCommandOptions{
				Commands: []string{
					"prlsp.resolveReviewThread",
					"prlsp.unresolveReviewThread",
					"prlsp.openReviewThreadInBrowser",
					"prlsp.createReviewComment",
					"prlsp.createReviewCommentRange",
					"prlsp.replyToReviewThread",
					"prlsp.refreshReviewThreads",
				},
			},
		},
		ServerInfo: lsp.ServerInfo{Name: "prlsp", Version: "0.1.0"},
	}
	s.conn.SendResponse(id, result)
}

func (s *Server) handleInitialized() {
	root := ""
	if s.rootURI != "" {
		root = uriToPath(s.rootURI)
	}
	if root == "" {
		log.Println("no workspace root found")
		return
	}

	s.gitInfo = git.Detect(root)
	if s.gitInfo == nil {
		log.Println("not a git repo or no GitHub remote")
		return
	}

	info := s.gitInfo
	prNumber, headSHA, ok := s.gh.FindPR(info.Owner, info.Repo, info.Branch)
	if !ok {
		log.Printf("no open PR for branch %s", info.Branch)
		return
	}
	s.prNumber = prNumber
	s.headSHA = headSHA
	log.Printf("found PR #%d for %s/%s branch %s", s.prNumber, info.Owner, info.Repo, info.Branch)

	s.refreshThreads()

	// Publish diagnostics for any already-open documents.
	for uri := range s.docs {
		s.publishFileDiagnostics(uri)
	}
}

func (s *Server) handleDidOpen(params json.RawMessage) {
	var p lsp.DidOpenTextDocumentParams
	json.Unmarshal(params, &p)
	s.docs[p.TextDocument.URI] = p.TextDocument.Text
	s.publishFileDiagnostics(p.TextDocument.URI)
}

func (s *Server) handleDidChange(params json.RawMessage) {
	var p lsp.DidChangeTextDocumentParams
	json.Unmarshal(params, &p)
	if len(p.ContentChanges) > 0 {
		s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
	}
}
