package github

import (
	"testing"
)

func TestNewMockLoadsFixture(t *testing.T) {
	mock, err := NewClientMock("testdata/threads.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if len(mock.Threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(mock.Threads))
	}

	byID := map[string]ReviewThread{}
	for _, tr := range mock.Threads {
		byID[tr.ThreadID] = tr
	}
	if !byID["T_resolved"].IsResolved {
		t.Error("T_resolved should be resolved")
	}
	if byID["T_unresolved"].IsResolved {
		t.Error("T_unresolved should be unresolved")
	}
}

func TestMockUnresolveRoundTrip(t *testing.T) {
	mock := &ClientMock{Threads: []ReviewThread{
		{ThreadID: "T1", Path: "a.go", Line: 1, IsResolved: false},
	}}

	if !mock.ResolveReviewThread("T1") || !mock.Threads[0].IsResolved {
		t.Fatal("resolve should set IsResolved=true")
	}
	if !mock.UnresolveReviewThread("T1") || mock.Threads[0].IsResolved {
		t.Fatal("unresolve should set IsResolved=false")
	}
	if mock.UnresolveReviewThread("nope") {
		t.Fatal("unresolving an unknown thread should return false")
	}
}

func TestMockCreateReviewCommentRange(t *testing.T) {
	mock := &ClientMock{}
	// Swapped bounds should anchor to the larger line.
	mock.CreateReviewCommentRange("o", "r", 1, "sha", "a.go", 9, 4, "body")
	if len(mock.Threads) != 1 {
		t.Fatalf("threads = %d, want 1", len(mock.Threads))
	}
	if mock.Threads[0].Line != 9 {
		t.Fatalf("line = %d, want 9", mock.Threads[0].Line)
	}
}

func TestParseReviewThreadsOutdated(t *testing.T) {
	// Models the real Staffbase/mops#17025 case: a resolved Copilot comment that
	// is outdated (line == null) but has an originalLine, alongside the other
	// combinations we must handle.
	const resp = `{
	  "data": { "repository": { "pullRequest": { "reviewThreads": { "nodes": [
	    { "id": "T_unresolved_current", "isResolved": false, "line": 10, "originalLine": 9, "path": "a.go",
	      "comments": { "nodes": [ { "databaseId": 1, "body": "fix", "author": { "login": "alice" } } ] } },
	    { "id": "T_resolved_current", "isResolved": true, "line": 20, "originalLine": 18, "path": "a.go",
	      "comments": { "nodes": [ { "databaseId": 2, "body": "done", "author": { "login": "bob" } } ] } },
	    { "id": "T_resolved_outdated", "isResolved": true, "line": null, "originalLine": 6, "path": "charts/mongodb/Chart.yaml",
	      "comments": { "nodes": [ { "databaseId": 3, "body": "appVersion still 8.3.2", "author": { "login": "copilot" } } ] } },
	    { "id": "T_unresolved_outdated", "isResolved": false, "line": null, "originalLine": 42, "path": "a.go",
	      "comments": { "nodes": [ { "databaseId": 4, "body": "stale", "author": { "login": "carol" } } ] } },
	    { "id": "T_resolved_noline", "isResolved": true, "line": null, "originalLine": null, "path": "a.go",
	      "comments": { "nodes": [ { "databaseId": 5, "body": "file level", "author": { "login": "dave" } } ] } }
	  ] } } } }
	}`

	threads := parseReviewThreads(resp)

	got := map[string]ReviewThread{}
	for i := range threads {
		got[threads[i].ThreadID] = threads[i]
	}

	if _, ok := got["T_unresolved_current"]; !ok {
		t.Error("unresolved current thread should be kept")
	}
	if _, ok := got["T_resolved_current"]; !ok {
		t.Error("resolved current thread should be kept")
	}

	rc, ok := got["T_resolved_outdated"]
	if !ok {
		t.Fatal("resolved outdated thread should be kept (regression: mops#17025)")
	}
	if rc.Line != 6 || !rc.IsOutdated || !rc.IsResolved {
		t.Errorf("resolved outdated = %+v, want line 6, outdated, resolved", rc)
	}

	uo, ok := got["T_unresolved_outdated"]
	if !ok {
		t.Fatal("unresolved outdated thread should be kept")
	}
	if uo.Line != 42 || !uo.IsOutdated || uo.IsResolved {
		t.Errorf("unresolved outdated = %+v, want line 42, outdated, unresolved", uo)
	}

	if _, ok := got["T_resolved_noline"]; ok {
		t.Error("thread with no line and no originalLine should be dropped")
	}

	if len(threads) != 4 {
		t.Fatalf("kept %d threads, want 4", len(threads))
	}
}
