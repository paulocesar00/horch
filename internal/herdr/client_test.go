package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type apiStep struct {
	method string
	params map[string]any
	result any
}

// scriptedAPI never connects to a user's server. Each request has its own socket connection.
func scriptedAPI(t *testing.T, steps []apiStep) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "pane-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, step := range steps {
			conn, err := ln.Accept()
			if err != nil {
				t.Errorf("missing request %s: %v", step.method, err)
				return
			}
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if req.Method != step.method || !reflect.DeepEqual(req.Params, step.params) {
				t.Errorf("request %s %+v; want %s %+v", req.Method, req.Params, step.method, step.params)
			}
			response := map[string]any{"result": step.result}
			if apiErr, ok := step.result.(*Error); ok {
				response = map[string]any{"error": apiErr}
			}
			_ = json.NewEncoder(conn).Encode(response)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return New(socket)
}

func TestPaneGetRejectsMissingWorkspace(t *testing.T) {
	for _, result := range []any{
		map[string]any{"pane": nil},
		map[string]any{"pane": map[string]any{"pane_id": "w1:p1", "workspace_id": nil}},
		map[string]any{"pane": map[string]any{"pane_id": "w1:p1"}},
	} {
		t.Run("missing workspace", func(t *testing.T) {
			c := scriptedAPI(t, []apiStep{{"pane.get", map[string]any{"pane_id": "w1:p1"}, result}})
			_, err := c.PaneGet(context.Background(), "w1:p1")
			if err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("want clear workspace error, got %v", err)
			}
		})
	}
}
