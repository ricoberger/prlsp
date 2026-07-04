// Package jsonrpc implements a minimal JSON-RPC 2.0 codec over an io.Reader /
// io.Writer pair using the LSP base protocol framing ("Content-Length" header
// followed by the JSON body). It is transport-agnostic and carries no
// knowledge of the LSP method set.
package jsonrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
)

// Message is a JSON-RPC 2.0 request, response, or notification.
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Conn reads and writes framed JSON-RPC messages. Writes are serialized so it
// is safe to send from multiple goroutines.
type Conn struct {
	reader *bufio.Reader
	mu     sync.Mutex
	writer *bufio.Writer
	nextID int
}

// New returns a Conn that reads from r and writes to w.
func New(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		reader: bufio.NewReader(r),
		writer: bufio.NewWriter(w),
		nextID: 1,
	}
}

// ReadMessage reads a single framed message from the underlying reader.
func (c *Conn) ReadMessage() (*Message, error) {
	// Read headers until an empty line.
	contentLength := -1
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(line[len("Content-Length:"):])
			n, err := strconv.Atoi(val)
			if err == nil {
				contentLength = n
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &msg, nil
}

func (c *Conn) writeJSON(msg any) {
	body, err := json.Marshal(msg)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.writer, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		log.Printf("write header error: %v", err)
		return
	}
	if _, err := c.writer.Write(body); err != nil {
		log.Printf("write body error: %v", err)
		return
	}
	if err := c.writer.Flush(); err != nil {
		log.Printf("flush error: %v", err)
	}
}

// SendResponse sends a response to the request identified by id. A nil result
// is encoded as JSON null.
func (c *Conn) SendResponse(id *json.RawMessage, result any) {
	var raw json.RawMessage
	if result == nil {
		raw = json.RawMessage("null")
	} else {
		var err error
		raw, err = json.Marshal(result)
		if err != nil {
			log.Printf("marshal result error: %v", err)
			return
		}
	}
	c.writeJSON(Message{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	})
}

// SendNotification sends a notification (a message without an id).
func (c *Conn) SendNotification(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		log.Printf("marshal notification params error: %v", err)
		return
	}
	c.writeJSON(Message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
	})
}

// SendRequest sends a request with an auto-incrementing id. The response is
// discarded by the read loop, so this is only useful for fire-and-forget
// requests where the result is not needed (e.g. window/showDocument).
func (c *Conn) SendRequest(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		log.Printf("marshal request params error: %v", err)
		return
	}
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()
	idRaw, _ := json.Marshal(id) // marshaling an int cannot fail
	rawID := json.RawMessage(idRaw)
	c.writeJSON(Message{
		JSONRPC: "2.0",
		ID:      &rawID,
		Method:  method,
		Params:  raw,
	})
}
