package jsonrpc

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestReadMessage(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"rootUri":"file:///x"}}`
	framed := "Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body

	c := New(strings.NewReader(framed), io.Discard)
	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg.Method != "initialize" {
		t.Fatalf("method = %q, want initialize", msg.Method)
	}

	// A second read hits EOF.
	if _, err := c.ReadMessage(); err == nil {
		t.Fatal("expected EOF error on second read")
	}
}

func TestReadMessageMissingContentLength(t *testing.T) {
	c := New(strings.NewReader("X-Foo: bar\r\n\r\n"), io.Discard)
	if _, err := c.ReadMessage(); err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

func TestSendResponseRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, &buf)

	id := json.RawMessage("7")
	c.SendResponse(&id, map[string]string{"hello": "world"})

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(*msg.ID) != "7" {
		t.Fatalf("id = %s, want 7", string(*msg.ID))
	}
	var result map[string]string
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["hello"] != "world" {
		t.Fatalf("result = %v", result)
	}
}

func TestSendResponseNull(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, &buf)
	id := json.RawMessage("1")
	c.SendResponse(&id, nil)

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(msg.Result) != "null" {
		t.Fatalf("result = %s, want null", string(msg.Result))
	}
}

func TestSendNotification(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, &buf)
	c.SendNotification("window/showMessage", map[string]any{"type": 3, "message": "hi"})

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg.Method != "window/showMessage" {
		t.Fatalf("method = %q", msg.Method)
	}
	if msg.ID != nil {
		t.Fatal("notification must not carry an id")
	}
	var p map[string]any
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if p["message"] != "hi" {
		t.Fatalf("message = %v", p["message"])
	}
}

func TestSendRequestIncrementsID(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, &buf)
	c.SendRequest("window/showDocument", map[string]any{"uri": "https://x"})
	c.SendRequest("window/showDocument", map[string]any{"uri": "https://y"})

	first, _ := c.ReadMessage()
	second, _ := c.ReadMessage()
	if first.ID == nil || second.ID == nil {
		t.Fatal("requests must carry ids")
	}
	if string(*first.ID) != "1" || string(*second.ID) != "2" {
		t.Fatalf("ids = %s, %s; want 1, 2", string(*first.ID), string(*second.ID))
	}
	if first.Method != "window/showDocument" {
		t.Fatalf("method = %q", first.Method)
	}
}

// itoa avoids importing strconv just for the framing helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
