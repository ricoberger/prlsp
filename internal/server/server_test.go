package server

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ricoberger/prlsp/internal/git"
	"github.com/ricoberger/prlsp/internal/github"
	"github.com/ricoberger/prlsp/internal/jsonrpc"
	"github.com/ricoberger/prlsp/internal/lsp"
)

// --- helpers ---

func newBufServer(gh github.Service) (*Server, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	s := New(jsonrpc.New(strings.NewReader(""), buf), gh)
	return s, buf
}

func newCmdServer(mock *github.ClientMock) (*Server, *bytes.Buffer) {
	s, buf := newBufServer(mock)
	s.gitInfo = &git.Info{Owner: "octo", Repo: "repo", Root: "/repo"}
	s.prNumber = 1
	s.headSHA = "headsha"
	s.threads = mock.Threads
	return s, buf
}

func readFrames(t *testing.T, buf *bytes.Buffer) []jsonrpc.Message {
	t.Helper()
	c := jsonrpc.New(bytes.NewReader(buf.Bytes()), io.Discard)
	var out []jsonrpc.Message
	for {
		m, err := c.ReadMessage()
		if err != nil {
			break
		}
		out = append(out, *m)
	}
	return out
}

func findByMethod(msgs []jsonrpc.Message, method string) *jsonrpc.Message {
	for i := range msgs {
		if msgs[i].Method == method {
			return &msgs[i]
		}
	}
	return nil
}

func lastShowMessage(msgs []jsonrpc.Message) string {
	text := ""
	for i := range msgs {
		if msgs[i].Method == "window/showMessage" {
			var p lsp.ShowMessageParams
			_ = json.Unmarshal(msgs[i].Params, &p)
			text = p.Message
		}
	}
	return text
}

func execParams(t *testing.T, cmd string, args ...any) json.RawMessage {
	t.Helper()
	raws := make([]json.RawMessage, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal arg %d: %v", i, err)
		}
		raws[i] = b
	}
	p, _ := json.Marshal(lsp.ExecuteCommandParams{Command: cmd, Arguments: raws})
	return p
}

func rawJSON(v any) *json.RawMessage {
	b, _ := json.Marshal(v)
	r := json.RawMessage(b)
	return &r
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// codeActionCommands runs handleCodeAction for a single prlsp diagnostic
// pointing at threadID and returns the resulting actions' command names.
func codeActionCommands(t *testing.T, s *Server, threadID string) []string {
	t.Helper()

	var buf bytes.Buffer
	s.conn = jsonrpc.New(strings.NewReader(""), &buf)

	params, _ := json.Marshal(lsp.CodeActionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: "file:///repo/a.go"},
		Range:        lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 0}},
		Context: lsp.CodeActionContext{
			Diagnostics: []lsp.Diagnostic{{
				Range:  lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 0}},
				Source: source,
				Data:   rawJSON(map[string]any{"thread_id": threadID, "comment_id": 99}),
			}},
		},
	})

	id := json.RawMessage("1")
	s.handleCodeAction(&id, params)

	var cmds []string
	for _, m := range readFrames(t, &buf) {
		if m.ID == nil {
			continue
		}
		var actions []lsp.CodeAction
		if err := json.Unmarshal(m.Result, &actions); err != nil {
			t.Fatalf("decode actions: %v", err)
		}
		for _, a := range actions {
			if a.Command != nil {
				cmds = append(cmds, a.Command.Command)
			}
		}
	}
	return cmds
}

// --- pure helpers ---

func TestUriToPath(t *testing.T) {
	cases := map[string]string{
		"file:///repo/a.go":      "/repo/a.go",
		"file:///repo/dir/b.go":  "/repo/dir/b.go",
		"plain/relative/path.go": "plain/relative/path.go",
		"file://":                "",
	}
	for in, want := range cases {
		if got := uriToPath(in); got != want {
			t.Errorf("uriToPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUriToRelpath(t *testing.T) {
	s := New(nil, nil)

	if got := s.uriToRelpath("file:///repo/a.go"); got != "/repo/a.go" {
		t.Fatalf("without gitInfo = %q, want /repo/a.go", got)
	}

	s.gitInfo = &git.Info{Root: "/repo"}
	if got := s.uriToRelpath("file:///repo/dir/a.go"); got != "dir/a.go" {
		t.Fatalf("with gitInfo = %q, want dir/a.go", got)
	}
	if got := s.uriToRelpath("file:///other/a.go"); got != "/other/a.go" {
		t.Fatalf("outside root = %q, want /other/a.go", got)
	}
}

func TestExtractSelection(t *testing.T) {
	doc := "alpha\nbravo\ncharlie\n"

	if got := extractSelection(doc, lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 5}}); got != "bravo" {
		t.Errorf("single line = %q, want bravo", got)
	}
	got := extractSelection(doc, lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 2, Character: 7}})
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "charlie") {
		t.Errorf("multi line = %q", got)
	}
	if got := extractSelection(doc, lsp.Range{Start: lsp.Position{Line: 99}, End: lsp.Position{Line: 99, Character: 1}}); got != "" {
		t.Errorf("out of range = %q, want empty", got)
	}
	if got := extractSelection("hi\n", lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 0, Character: 999}}); got != "hi" {
		t.Errorf("clamped = %q, want hi", got)
	}
}

func TestPreviewBody(t *testing.T) {
	// Short body: returned as-is, no ellipsis.
	if got := previewBody("LGTM"); got != "LGTM" {
		t.Errorf("short body = %q, want LGTM", got)
	}
	// Newlines collapse to spaces and surrounding space is trimmed.
	if got := previewBody("  line1\nline2  "); got != "line1 line2" {
		t.Errorf("multiline body = %q, want 'line1 line2'", got)
	}
	// Long body: truncated to commentPreviewLen runes with an ellipsis.
	long := strings.Repeat("a", commentPreviewLen+10)
	if got := previewBody(long); got != strings.Repeat("a", commentPreviewLen)+"..." {
		t.Errorf("long body = %q", got)
	}
	// Truncation must not split a multi-byte rune.
	if got := previewBody(strings.Repeat("é", commentPreviewLen+5)); !utf8.ValidString(got) {
		t.Errorf("truncated body is not valid UTF-8: %q", got)
	}
}

func TestMakeDiagnosticData(t *testing.T) {
	th := github.ReviewThread{
		ThreadID:   "T1",
		Path:       "a.go",
		Line:       12,
		IsOutdated: true,
		Comments: []github.ReviewComment{
			{DatabaseID: 5, Author: "alice", Body: "first"},
			{DatabaseID: 6, Author: "bob", Body: "second"},
		},
	}
	var data map[string]any
	if err := json.Unmarshal(*makeDiagnostic(&th).Data, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["line"].(float64) != 12 {
		t.Errorf("line = %v, want 12", data["line"])
	}
	if data["outdated"] != true {
		t.Errorf("outdated = %v, want true", data["outdated"])
	}
	if data["comment_id"].(float64) != 5 {
		t.Errorf("comment_id = %v, want 5 (first comment)", data["comment_id"])
	}
	comments, ok := data["comments"].([]any)
	if !ok || len(comments) != 2 {
		t.Fatalf("comments = %v, want 2 entries", data["comments"])
	}
	first := comments[0].(map[string]any)
	if first["author"] != "alice" || first["body"] != "first" {
		t.Errorf("first comment = %v, want {alice, first}", first)
	}
}

func TestExtractDiagData(t *testing.T) {
	direct := json.RawMessage(`{"thread_id":"T1","comment_id":5}`)
	if m := extractDiagData(&direct); m == nil || m["thread_id"] != "T1" {
		t.Fatalf("direct form failed: %v", m)
	}
	wrapped := json.RawMessage(`{"data":{"thread_id":"T2","comment_id":6}}`)
	if m := extractDiagData(&wrapped); m == nil || m["thread_id"] != "T2" {
		t.Fatalf("wrapped form failed: %v", m)
	}
	if m := extractDiagData(nil); m != nil {
		t.Fatalf("nil = %v, want nil", m)
	}
	bad := json.RawMessage(`{not json`)
	if m := extractDiagData(&bad); m != nil {
		t.Fatalf("invalid json = %v, want nil", m)
	}
}

func TestMakeDiagnosticSeverity(t *testing.T) {
	cases := []struct {
		name string
		t    github.ReviewThread
		want int
	}{
		{"unresolved current", github.ReviewThread{ThreadID: "1", Line: 5}, lsp.SeverityError},
		{"unresolved outdated", github.ReviewThread{ThreadID: "2", Line: 5, IsOutdated: true}, lsp.SeverityWarning},
		{"resolved", github.ReviewThread{ThreadID: "3", Line: 5, IsResolved: true}, lsp.SeverityInformation},
		{"resolved outdated", github.ReviewThread{ThreadID: "4", Line: 5, IsResolved: true, IsOutdated: true}, lsp.SeverityInformation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := makeDiagnostic(&c.t)
			if d.Severity != c.want {
				t.Fatalf("severity = %d, want %d", d.Severity, c.want)
			}
			var data map[string]any
			if err := json.Unmarshal(*d.Data, &data); err != nil {
				t.Fatalf("unmarshal data: %v", err)
			}
			if data["resolved"] != c.t.IsResolved {
				t.Fatalf("data.resolved = %v, want %v", data["resolved"], c.t.IsResolved)
			}
		})
	}
}

// --- document sync ---

func TestHandleDidOpenPublishesDiagnostics(t *testing.T) {
	s, buf := newBufServer(&github.ClientMock{})
	s.gitInfo = &git.Info{Root: "/repo"}
	s.threads = []github.ReviewThread{
		{ThreadID: "T1", Path: "a.go", Line: 2, Comments: []github.ReviewComment{{DatabaseID: 1, Body: "x", Author: "a"}}},
	}

	params, _ := json.Marshal(lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: "file:///repo/a.go", Text: "l1\nl2\nl3\n"},
	})
	s.handleDidOpen(params)

	if s.docs["file:///repo/a.go"] == "" {
		t.Fatal("didOpen should store document content")
	}
	pub := findByMethod(readFrames(t, buf), "textDocument/publishDiagnostics")
	if pub == nil {
		t.Fatal("didOpen should publish diagnostics")
	}
	var pp lsp.PublishDiagnosticsParams
	if err := json.Unmarshal(pub.Params, &pp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pp.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(pp.Diagnostics))
	}
}

func TestHandleDidChangeUpdatesDoc(t *testing.T) {
	s, _ := newBufServer(&github.ClientMock{})
	params, _ := json.Marshal(lsp.DidChangeTextDocumentParams{
		TextDocument:   lsp.VersionedTextDocumentIdentifier{URI: "file:///repo/a.go", Version: 2},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{{Text: "updated"}},
	})
	s.handleDidChange(params)
	if s.docs["file:///repo/a.go"] != "updated" {
		t.Fatalf("didChange stored %q, want updated", s.docs["file:///repo/a.go"])
	}
}

// --- initialize ---

func TestHandleInitialize(t *testing.T) {
	s, buf := newBufServer(&github.ClientMock{})
	params := json.RawMessage(`{"workspaceFolders":[{"uri":"file:///repo","name":"repo"}]}`)
	id := json.RawMessage("1")
	s.handleInitialize(&id, params)

	if s.rootURI != "file:///repo" {
		t.Fatalf("rootURI = %q", s.rootURI)
	}
	frames := readFrames(t, buf)
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	var res lsp.InitializeResult
	if err := json.Unmarshal(frames[0].Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.ServerInfo.Name != "prlsp" {
		t.Fatalf("server name = %q", res.ServerInfo.Name)
	}
	if !res.Capabilities.CodeActionProvider {
		t.Fatal("codeActionProvider should be true")
	}
	cmds := res.Capabilities.ExecuteCommandProvider.Commands
	for _, want := range []string{"prlsp.resolveReviewThread", "prlsp.unresolveReviewThread", "prlsp.refreshReviewThreads"} {
		if !containsStr(cmds, want) {
			t.Errorf("capabilities missing command %q (got %v)", want, cmds)
		}
	}
}

// --- code actions ---

func TestCodeActionResolveVsUnresolve(t *testing.T) {
	mock := &github.ClientMock{}
	s, _ := newBufServer(mock)
	s.gitInfo = &git.Info{Owner: "octo", Repo: "repo", Root: "/repo"}
	s.prNumber = 1
	s.threads = []github.ReviewThread{
		{ThreadID: "T_open", Path: "a.go", Line: 1, IsResolved: false,
			Comments: []github.ReviewComment{{DatabaseID: 10, Body: "fix", Author: "alice"}}},
		{ThreadID: "T_done", Path: "a.go", Line: 2, IsResolved: true,
			Comments: []github.ReviewComment{{DatabaseID: 20, Body: "done", Author: "bob"}}},
	}

	open := codeActionCommands(t, s, "T_open")
	if !containsStr(open, "prlsp.resolveReviewThread") || containsStr(open, "prlsp.unresolveReviewThread") {
		t.Errorf("unresolved thread commands = %v", open)
	}

	done := codeActionCommands(t, s, "T_done")
	if !containsStr(done, "prlsp.unresolveReviewThread") || containsStr(done, "prlsp.resolveReviewThread") {
		t.Errorf("resolved thread commands = %v", done)
	}
}

// --- executeCommand / cmd* ---

func TestHandleExecuteCommandResolve(t *testing.T) {
	mock := &github.ClientMock{Threads: []github.ReviewThread{
		{ThreadID: "T1", Path: "a.go", Line: 1, IsResolved: false,
			Comments: []github.ReviewComment{{DatabaseID: 5, Body: "fix", Author: "a"}}},
	}}
	s, buf := newCmdServer(mock)

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.resolveReviewThread", "T1", "file:///repo/a.go"))

	if !mock.Threads[0].IsResolved {
		t.Fatal("thread should be resolved in the backend")
	}
	if msg := lastShowMessage(readFrames(t, buf)); msg != "Review thread resolved" {
		t.Fatalf("showMessage = %q", msg)
	}
}

func TestHandleExecuteCommandUnresolve(t *testing.T) {
	mock := &github.ClientMock{Threads: []github.ReviewThread{
		{ThreadID: "T1", Path: "a.go", Line: 1, IsResolved: true,
			Comments: []github.ReviewComment{{DatabaseID: 5, Body: "fix", Author: "a"}}},
	}}
	s, buf := newCmdServer(mock)

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.unresolveReviewThread", "T1", "file:///repo/a.go"))

	if mock.Threads[0].IsResolved {
		t.Fatal("thread should be unresolved in the backend")
	}
	if msg := lastShowMessage(readFrames(t, buf)); msg != "Review thread unresolved" {
		t.Fatalf("showMessage = %q", msg)
	}
}

func TestHandleExecuteCommandCreateComment(t *testing.T) {
	mock := &github.ClientMock{}
	s, buf := newCmdServer(mock)

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.createReviewComment", "file:///repo/a.go", 12, "looks off"))

	if len(mock.Threads) != 1 {
		t.Fatalf("mock threads = %d, want 1 created", len(mock.Threads))
	}
	if mock.Threads[0].Path != "a.go" || mock.Threads[0].Line != 12 {
		t.Fatalf("created thread = %+v", mock.Threads[0])
	}
	if msg := lastShowMessage(readFrames(t, buf)); msg != "Review comment posted on L12" {
		t.Fatalf("showMessage = %q", msg)
	}
}

func TestHandleExecuteCommandCreateCommentRange(t *testing.T) {
	mock := &github.ClientMock{}
	s, buf := newCmdServer(mock)

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.createReviewCommentRange", "file:///repo/a.go", 3, 6, "range comment"))

	if len(mock.Threads) != 1 {
		t.Fatalf("mock threads = %d, want 1 created", len(mock.Threads))
	}
	if msg := lastShowMessage(readFrames(t, buf)); msg != "Review comment posted on L3-L6" {
		t.Fatalf("showMessage = %q", msg)
	}
}

func TestHandleExecuteCommandReply(t *testing.T) {
	mock := &github.ClientMock{Threads: []github.ReviewThread{
		{ThreadID: "T1", Path: "a.go", Line: 1,
			Comments: []github.ReviewComment{{DatabaseID: 5, Body: "orig", Author: "a"}}},
	}}
	s, buf := newCmdServer(mock)

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.replyToReviewThread", 5, "file:///repo/a.go", "my reply"))

	if len(mock.Threads[0].Comments) != 2 {
		t.Fatalf("comments = %d, want 2 after reply", len(mock.Threads[0].Comments))
	}
	if msg := lastShowMessage(readFrames(t, buf)); msg != "Reply posted" {
		t.Fatalf("showMessage = %q", msg)
	}
}

func TestHandleExecuteCommandOpenInBrowser(t *testing.T) {
	s, buf := newCmdServer(&github.ClientMock{})
	url := "https://github.com/octo/repo/pull/1#discussion_r5"

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.openReviewThreadInBrowser", url))

	req := findByMethod(readFrames(t, buf), "window/showDocument")
	if req == nil {
		t.Fatal("expected a window/showDocument request")
	}
	var p lsp.ShowDocumentParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.URI != url || !p.External {
		t.Fatalf("showDocument params = %+v", p)
	}
}

func TestHandleExecuteCommandRefresh(t *testing.T) {
	mock := &github.ClientMock{Threads: []github.ReviewThread{
		{ThreadID: "T1", Path: "a.go", Line: 1, Comments: []github.ReviewComment{{DatabaseID: 5, Body: "x", Author: "a"}}},
	}}
	s, buf := newCmdServer(mock)
	s.docs["file:///repo/a.go"] = "l1\n"

	id := json.RawMessage("1")
	s.handleExecuteCommand(&id, execParams(t, "prlsp.refreshReviewThreads"))

	frames := readFrames(t, buf)
	if findByMethod(frames, "textDocument/publishDiagnostics") == nil {
		t.Fatal("refresh should republish diagnostics for open docs")
	}
	if msg := lastShowMessage(frames); msg != "Review threads refreshed" {
		t.Fatalf("showMessage = %q", msg)
	}
}

func TestRefreshThreadsNoOpWithoutPR(t *testing.T) {
	s, _ := newBufServer(&github.ClientMock{Threads: []github.ReviewThread{{ThreadID: "X"}}})
	s.threads = nil
	s.refreshThreads()
	if s.threads != nil {
		t.Fatalf("refreshThreads should be a no-op without a PR, got %v", s.threads)
	}
}
