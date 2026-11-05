// Synthetic upstream for the isolated route lab. Never contacts another host.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var mode atomic.Value
var count atomic.Int64

func main() {
	mode.Store("normal")
	http.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			mode.Store(r.URL.Query().Get("mode"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": mode.Load(), "requests": count.Load()})
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		var in struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
		m := mode.Load().(string)
		if m == "fail-first" && strings.Contains(r.Header.Get("Authorization"), "synthetic-first") {
			w.Header().Set("X-Request-ID", fmt.Sprintf("mock-failed-%d", n))
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"synthetic upstream failure"}}`)
			return
		}
		if m == "timeout" {
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		if m != "no-id" {
			w.Header().Set("X-Request-ID", fmt.Sprintf("mock-provider-%d", n))
		}
		response := map[string]any{"id": fmt.Sprintf("resp_mock_%d", n), "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": in.Model, "output": []any{map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "synthetic route response", "annotations": []any{}}}}}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}
		if !in.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": response["id"], "object": "response", "model": in.Model, "status": "in_progress", "output": []any{}}},
			{"type": "response.output_text.delta", "item_id": "msg_mock", "output_index": 0, "content_index": 0, "delta": "synthetic route response"},
			{"type": "response.completed", "response": response},
		} {
			b, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], b)
			w.(http.Flusher).Flush()
		}
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
