// Package server implements the prlsp language server: it holds the session
// state, runs the JSON-RPC read loop, and handles LSP requests by surfacing
// GitHub pull-request review threads as diagnostics and code actions.
package server

import (
	"encoding/json"
	"io"
	"log"
	"sync"

	"github.com/ricoberger/prlsp/internal/git"
	"github.com/ricoberger/prlsp/internal/github"
	"github.com/ricoberger/prlsp/internal/jsonrpc"
	"github.com/ricoberger/prlsp/internal/lsp"
)

// Server holds the language server's session state.
//
// The JSON-RPC read loop is single-threaded, but session detection
// (git.Detect + gh lookups) runs on its own goroutine so a slow git/gh call
// does not stall message handling. mu guards every mutable field below so the
// two goroutines never race; conn and gh are set once in New and are safe to
// use without it.
type Server struct {
	conn     *jsonrpc.Conn
	gh       github.Service
	mu       sync.Mutex
	docs     map[string]string // uri -> full content
	gitInfo  *git.Info
	prNumber int
	headSHA  string
	threads  []github.ReviewThread
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

	// Save the workspace root for later. Prefer workspaceFolders, then the
	// (deprecated) rootUri, then the (also deprecated) rootPath, which is a
	// plain filesystem path rather than a URI.
	s.mu.Lock()
	if len(p.WorkspaceFolders) > 0 {
		s.rootURI = p.WorkspaceFolders[0].URI
	} else if p.RootURI != "" {
		s.rootURI = p.RootURI
	} else if p.RootPath != "" {
		s.rootURI = "file://" + p.RootPath
	}
	s.mu.Unlock()

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
	s.mu.Lock()
	rootURI := s.rootURI
	s.mu.Unlock()

	root := ""
	if rootURI != "" {
		root = uriToPath(rootURI)
	}
	if root == "" {
		log.Println("no workspace root found")
		return
	}

	// git.Detect and the gh lookups can be slow; run them off the read loop so
	// the server stays responsive to other messages while it starts up.
	go s.initSession(root)
}

// initSession detects the git/PR context and loads review threads. It performs
// the slow git/gh work without holding s.mu and only takes the lock to publish
// the results, so it never blocks the read loop.
func (s *Server) initSession(root string) {
	info := git.Detect(root)
	if info == nil {
		log.Println("not a git repo or no GitHub remote")
		return
	}
	prNumber, headSHA, ok := s.gh.FindPR(info.Owner, info.Repo, info.Branch)
	if !ok {
		log.Printf("no open PR for branch %s", info.Branch)
		return
	}
	log.Printf("found PR #%d for %s/%s branch %s", prNumber, info.Owner, info.Repo, info.Branch)

	threads, err := s.gh.FetchReviewThreads(info.Owner, info.Repo, prNumber)
	if err != nil {
		log.Printf("failed to fetch review threads: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gitInfo = info
	s.prNumber = prNumber
	s.headSHA = headSHA
	if err == nil {
		s.threads = threads
		unresolved := 0
		for _, t := range s.threads {
			if !t.IsResolved {
				unresolved++
			}
		}
		log.Printf("fetched %d review threads (%d unresolved)", len(s.threads), unresolved)
	}

	// Publish diagnostics for any already-open documents.
	for uri := range s.docs {
		s.publishFileDiagnostics(uri)
	}
}

func (s *Server) handleDidOpen(params json.RawMessage) {
	var p lsp.DidOpenTextDocumentParams
	json.Unmarshal(params, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[p.TextDocument.URI] = p.TextDocument.Text
	s.publishFileDiagnostics(p.TextDocument.URI)
}

func (s *Server) handleDidChange(params json.RawMessage) {
	var p lsp.DidChangeTextDocumentParams
	json.Unmarshal(params, &p)
	if len(p.ContentChanges) > 0 {
		s.mu.Lock()
		s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
		s.mu.Unlock()
	}
}
