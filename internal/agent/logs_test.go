package agent

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logAgent returns an agent that keeps logs under a temporary
// directory, with one pod's log already written.
func logAgent(t *testing.T, contents string) (*Agent, string) {
	t.Helper()
	agent := New(Config{Log: log.New(io.Discard, "", 0), StateDir: t.TempDir()})
	agent.ctx = context.Background()
	file, err := agent.openLog("shop", "web-1", "uid-1")
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(contents)
	file.Close()
	return agent, filepath.Join(agent.config.StateDir, logsDir, "uid-1"+logSuffix)
}

// TestLogs checks the kubelet's log request: a pod's whole log, the
// last lines of it when the request asks for a tail, and not found for
// a pod the agent has no log of.
func TestLogs(t *testing.T) {
	agent, _ := logAgent(t, "one\ntwo\nthree\n")
	handler := agent.KubeletHandler()

	whole := get(handler, "/containerLogs/shop/web-1/main", nil)
	if whole.Code != http.StatusOK || whole.Body.String() != "one\ntwo\nthree\n" {
		t.Errorf("whole: %d %q", whole.Code, whole.Body.String())
	}

	tail := get(handler, "/containerLogs/shop/web-1/main?tailLines=2", nil)
	if tail.Body.String() != "two\nthree\n" {
		t.Errorf("tail of 2: %q", tail.Body.String())
	}
	more := get(handler, "/containerLogs/shop/web-1/main?tailLines=10", nil)
	if more.Body.String() != "one\ntwo\nthree\n" {
		t.Errorf("tail of 10: %q", more.Body.String())
	}

	if missing := get(handler, "/containerLogs/shop/other/main", nil); missing.Code != http.StatusNotFound {
		t.Errorf("another pod: %d", missing.Code)
	}
}

// TestLogsFollow checks that a request that follows the log gets what
// is written after the request began, and ends when the client goes.
func TestLogsFollow(t *testing.T) {
	agent, path := logAgent(t, "first\n")
	server := httptest.NewServer(agent.KubeletHandler())
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/containerLogs/shop/web-1/main?follow=true", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	// A line written while the request is open.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("second\n")
	file.Close()

	read := make(chan string, 1)
	go func() {
		buffer := make([]byte, 64)
		var got strings.Builder
		for !strings.Contains(got.String(), "second\n") {
			n, err := response.Body.Read(buffer)
			got.Write(buffer[:n])
			if err != nil {
				break
			}
		}
		read <- got.String()
	}()
	select {
	case got := <-read:
		if got != "first\nsecond\n" {
			t.Errorf("followed: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("the line written later never arrived")
	}
}

// TestForgetLog checks that a pod's log is gone from disk and from the
// agent once the pod is forgotten.
func TestForgetLog(t *testing.T) {
	agent, path := logAgent(t, "one\n")
	agent.forgetLog("shop", "web-1", "uid-1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the log is still there: %v", err)
	}
	if missing := get(agent.KubeletHandler(), "/containerLogs/shop/web-1/main", nil); missing.Code != http.StatusNotFound {
		t.Errorf("after forgetting: %d", missing.Code)
	}
}

// TestLastLines checks where the last lines of a text start, with and
// without a newline at the end, and with fewer lines than asked for.
func TestLastLines(t *testing.T) {
	cases := []struct {
		text  string
		lines int
		want  string
	}{
		{"a\nb\nc\n", 1, "c\n"},
		{"a\nb\nc", 1, "c"},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc\n", 5, "a\nb\nc\n"},
		{"", 3, ""},
		{"a\nb\n", 0, ""},
	}
	for _, c := range cases {
		if got := c.text[lastLines([]byte(c.text), c.lines):]; got != c.want {
			t.Errorf("last %d of %q: %q, want %q", c.lines, c.text, got, c.want)
		}
	}
}
