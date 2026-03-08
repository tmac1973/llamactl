package api

import (
	"fmt"
	"net/http"
)

// SSEWriter wraps an http.ResponseWriter for Server-Sent Events.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	return &SSEWriter{w: w, flusher: flusher}, nil
}

func (s *SSEWriter) SendEvent(event, data string) error {
	if event != "" {
		fmt.Fprintf(s.w, "event: %s\n", event)
	}
	fmt.Fprintf(s.w, "data: %s\n\n", data)
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) SendData(data string) error {
	return s.SendEvent("", data)
}

// SendLine sends data with a trailing newline embedded using multi-line SSE format.
// SSE spec: each "data:" line appends value+LF to the buffer; trailing LF is stripped.
// So "data: X\ndata:\n\n" produces event data = "X\n".
func (s *SSEWriter) SendLine(data string) error {
	fmt.Fprintf(s.w, "data: %s\ndata:\n\n", data)
	s.flusher.Flush()
	return nil
}
