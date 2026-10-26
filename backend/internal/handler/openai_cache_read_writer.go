package handler

import (
	"bytes"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// installCacheReadWriter leaves upstream bytes/usage untouched and transforms
// only the final HTTP wire representation (including protocol bridge output).
func installCacheReadWriter(c *gin.Context, key *service.APIKey) func() {
	percent := service.OpenAICacheReadDeductionPercent(key)
	if percent == 0 || strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		return func() {}
	}
	if _, exists := c.Writer.(*cacheReadResponseWriter); exists {
		return func() {}
	}
	original := c.Writer
	w := &cacheReadResponseWriter{ResponseWriter: original, transform: service.NewCacheReadWireTransformer(percent, strings.Contains(c.Request.URL.Path, "/messages"))}
	c.Writer = w
	return func() { w.finish(); c.Writer = original }
}

type cacheReadResponseWriter struct {
	gin.ResponseWriter
	transform *service.CacheReadWireTransformer
	pending   []byte
	consumed  int
	wrote     bool
	writeErr  error
}

func (w *cacheReadResponseWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	w.Header().Del("Content-Length")
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	if !strings.Contains(contentType, "json") && !strings.Contains(contentType, "event-stream") {
		return w.ResponseWriter.Write(p)
	}
	w.wrote = true
	w.consumed += len(p)
	w.pending = append(w.pending, p...)
	if strings.Contains(contentType, "event-stream") {
		for {
			end, width := bytes.Index(w.pending, []byte("\n\n")), 2
			if crlf := bytes.Index(w.pending, []byte("\r\n\r\n")); crlf >= 0 && (end < 0 || crlf < end) {
				end, width = crlf, 4
			}
			if end < 0 {
				break
			}
			event := w.transform.TransformSSEEvent(w.pending[:end+width])
			if _, err := w.ResponseWriter.Write(event); err != nil {
				w.writeErr = err
				return 0, err
			}
			w.pending = w.pending[end+width:]
		}
	} else if gjson.ValidBytes(w.pending) {
		// The usual non-streaming path writes a complete JSON body at once.
		// Send it here so downstream disconnects are still reported to forwarding
		// code, rather than hidden by a deferred write after usage is scheduled.
		payload := w.transform.Transform(w.pending)
		w.pending = nil
		if _, err := w.ResponseWriter.Write(payload); err != nil {
			w.writeErr = err
			return 0, err
		}
	}
	return len(p), nil
}
func (w *cacheReadResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *cacheReadResponseWriter) Written() bool                     { return w.wrote || w.ResponseWriter.Written() }
func (w *cacheReadResponseWriter) Size() int {
	if w.wrote {
		return w.consumed
	}
	return w.ResponseWriter.Size()
}
func (w *cacheReadResponseWriter) Flush() { w.Header().Del("Content-Length"); w.ResponseWriter.Flush() }
func (w *cacheReadResponseWriter) WriteHeaderNow() {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeaderNow()
}
func (w *cacheReadResponseWriter) finish() {
	if len(w.pending) == 0 || w.writeErr != nil {
		return
	}
	w.Header().Del("Content-Length")
	payload := w.pending
	if strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "event-stream") {
		payload = w.transform.TransformSSEEvent(payload)
	} else {
		payload = w.transform.Transform(payload)
	}
	_, _ = w.ResponseWriter.Write(payload)
	w.pending = nil
}
