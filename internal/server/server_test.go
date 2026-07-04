package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
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
	// A sibling directory that merely shares the root as a string prefix must
	// not be treated as being inside the root.
	if got := s.uriToRelpath("file:///repo-backup/a.go"); got != "/repo-backup/a.go" {
		t.Fatalf("sibling dir = %q, want /repo-backup/a.go", got)
	}
	// The root itself maps to the empty relative path.
	if got := s.uriToRelpath("file:///repo"); got != "" {
		t.Fatalf("root itself = %q, want empty", got)
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
	// Character offsets are UTF-16 code units, not bytes: selecting "llo" from
	// "héllo" spans UTF-16 offsets 2..5 even though é is two bytes.
	if got := extractSelection("héllo\n", lsp.Range{Start: lsp.Position{Line: 0, Character: 2}, End: lsp.Position{Line: 0, Character: 5}}); got != "llo" {
		t.Errorf("utf16 selection = %q, want llo", got)
	}
	// A surrogate pair (emoji) counts as two UTF-16 units; selecting the "b"
	// after it must land past all four bytes of the emoji.
	if got := extractSelection("a😀b\n", lsp.Range{Start: lsp.Position{Line: 0, Character: 3}, End: lsp.Position{Line: 0, Character: 4}}); got != "b" {
		t.Errorf("emoji selection = %q, want b", got)
	}
	// Multi-line selection with multi-byte runes on both the start and end
	// lines exercises the UTF-16 conversion in the multi-line branch.
	if got := extractSelection("héllo\nwörld\n", lsp.Range{Start: lsp.Position{Line: 0, Character: 2}, End: lsp.Position{Line: 1, Character: 3}}); got != "llo\nwör" {
		t.Errorf("multi-line utf16 selection = %q, want \"llo\\nwör\"", got)
	}
}

func TestUTF16OffsetToByte(t *testing.T) {
	// "a😀b": bytes a=0, 😀=1..4 (4 bytes), b=5, len 6.
	// UTF-16 units:  a=1, 😀=2, b=1.
	// "héllo": bytes h=0, é=1..2 (2 bytes), l=3, l=4, o=5, len 6.
	cases := []struct {
		name   string
		line   string
		offset int
		want   int
	}{
		{"negative clamps to 0", "abc", -3, 0},
		{"zero", "abc", 0, 0},
		{"ascii mid", "abc", 2, 2},
		{"ascii past end clamps to len", "abc", 99, 3},
		{"empty line", "", 5, 0},

		{"before emoji", "a😀b", 1, 1},
		{"inside surrogate pair rounds up", "a😀b", 2, 5},
		{"after emoji", "a😀b", 3, 5},
		{"end after emoji", "a😀b", 4, 6},
		{"past end after emoji", "a😀b", 100, 6},

		{"start of 2-byte rune", "héllo", 1, 1},
		{"after 2-byte rune", "héllo", 2, 3},
		{"end of 2-byte line", "héllo", 5, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := utf16OffsetToByte(c.line, c.offset); got != c.want {
				t.Fatalf("utf16OffsetToByte(%q, %d) = %d, want %d", c.line, c.offset, got, c.want)
			}
		})
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
		if !slices.Contains(cmds, want) {
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
	if !slices.Contains(open, "prlsp.resolveReviewThread") || slices.Contains(open, "prlsp.unresolveReviewThread") {
		t.Errorf("unresolved thread commands = %v", open)
	}

	done := codeActionCommands(t, s, "T_done")
	if !slices.Contains(done, "prlsp.unresolveReviewThread") || slices.Contains(done, "prlsp.resolveReviewThread") {
		t.Errorf("resolved thread commands = %v", done)
	}
}

func TestCodeActionSingleVsMultiLineComment(t *testing.T) {
	newServer := func() (*Server, string) {
		s, _ := newBufServer(&github.ClientMock{})
		s.gitInfo = &git.Info{Owner: "octo", Repo: "repo", Root: "/repo"}
		s.prNumber = 1
		s.headSHA = "headsha"
		uri := "file:///repo/a.go"
		s.docs[uri] = "line0\nline1\nline2\n"
		return s, uri
	}

	// Runs handleCodeAction over the given range and returns the emitted
	// "create" review-comment action.
	createAction := func(t *testing.T, s *Server, uri string, r lsp.Range) lsp.CodeAction {
		t.Helper()
		var buf bytes.Buffer
		s.conn = jsonrpc.New(strings.NewReader(""), &buf)
		params, _ := json.Marshal(lsp.CodeActionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Range:        r,
		})
		id := json.RawMessage("1")
		s.handleCodeAction(&id, params)
		for _, m := range readFrames(t, &buf) {
			if m.ID == nil {
				continue
			}
			var actions []lsp.CodeAction
			if err := json.Unmarshal(m.Result, &actions); err != nil {
				t.Fatalf("decode actions: %v", err)
			}
			for _, a := range actions {
				if a.Command != nil && strings.HasPrefix(a.Command.Command, "prlsp.createReviewComment") {
					return a
				}
			}
		}
		t.Fatal("no create-review-comment action emitted")
		return lsp.CodeAction{}
	}

	t.Run("single line", func(t *testing.T) {
		s, uri := newServer()
		a := createAction(t, s, uri, lsp.Range{
			Start: lsp.Position{Line: 1, Character: 0},
			End:   lsp.Position{Line: 1, Character: 5},
		})
		if a.Command.Command != "prlsp.createReviewComment" {
			t.Fatalf("command = %q, want prlsp.createReviewComment", a.Command.Command)
		}
		// Arguments: [uri, line, body]; line is 1-indexed.
		if got := a.Command.Arguments[1].(float64); got != 2 {
			t.Fatalf("line = %v, want 2", got)
		}
	})

	t.Run("multi line", func(t *testing.T) {
		s, uri := newServer()
		a := createAction(t, s, uri, lsp.Range{
			Start: lsp.Position{Line: 0, Character: 0},
			End:   lsp.Position{Line: 2, Character: 5},
		})
		if a.Command.Command != "prlsp.createReviewCommentRange" {
			t.Fatalf("command = %q, want prlsp.createReviewCommentRange", a.Command.Command)
		}
		// Arguments: [uri, startLine, endLine, body]; both 1-indexed.
		if got := a.Command.Arguments[1].(float64); got != 1 {
			t.Fatalf("startLine = %v, want 1", got)
		}
		if got := a.Command.Arguments[2].(float64); got != 3 {
			t.Fatalf("endLine = %v, want 3", got)
		}
	})

	t.Run("multi line ending at column zero", func(t *testing.T) {
		s, uri := newServer()
		// A selection ending at the start of line 2 only covers lines 0-1.
		a := createAction(t, s, uri, lsp.Range{
			Start: lsp.Position{Line: 0, Character: 0},
			End:   lsp.Position{Line: 2, Character: 0},
		})
		if a.Command.Command != "prlsp.createReviewCommentRange" {
			t.Fatalf("command = %q, want prlsp.createReviewCommentRange", a.Command.Command)
		}
		if got := a.Command.Arguments[2].(float64); got != 2 {
			t.Fatalf("endLine = %v, want 2", got)
		}
	})
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

// errFetchClient reuses the mock but fails every FetchReviewThreads call.
type errFetchClient struct {
	*github.ClientMock
}

func (errFetchClient) FetchReviewThreads(owner, repo string, pr int) ([]github.ReviewThread, error) {
	return nil, errors.New("gh unavailable")
}

func TestRefreshThreadsKeepsCacheOnError(t *testing.T) {
	s, _ := newBufServer(errFetchClient{&github.ClientMock{}})
	s.gitInfo = &git.Info{Owner: "octo", Repo: "repo", Root: "/repo"}
	s.prNumber = 1
	cached := []github.ReviewThread{{ThreadID: "T1", Path: "a.go", Line: 1}}
	s.threads = cached

	s.refreshThreads()

	if len(s.threads) != 1 || s.threads[0].ThreadID != "T1" {
		t.Fatalf("threads = %v, want cached threads kept on fetch error", s.threads)
	}
}
