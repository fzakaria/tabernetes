package agent

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Where pods' logs are kept under the agent's state directory, one file
// a pod, named for the pod's UID.
const (
	logsDir   = "logs"
	logSuffix = ".log"
)

// followEvery is how often a request that follows a log looks for more
// of it.
const followEvery = 250 * time.Millisecond

// The kubelet's log request, as the API server makes it for `kubectl
// logs`: the path, and the two parameters the agent honours.
const (
	pathContainerLogs = "GET /containerLogs/{namespace}/{pod}/{container}"
	queryFollow       = "follow"
	queryTailLines    = "tailLines"
)

// podKey names a pod the way the kubelet's requests do.
func podKey(namespace, name string) string {
	return namespace + "/" + name
}

// openLog opens a pod's log for appending and remembers where the log
// is. A log outlives the pod's run: `kubectl logs` reads a finished
// pod's output for as long as the pod is in the API server.
func (a *Agent) openLog(namespace, name, uid string) (*os.File, error) {
	dir := filepath.Join(a.config.StateDir, logsDir)
	if err := os.MkdirAll(dir, podDirMode); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, uid+logSuffix)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	if a.logs == nil {
		a.logs = map[string]string{}
	}
	a.logs[podKey(namespace, name)] = path
	a.mu.Unlock()
	return file, nil
}

// forgetLog removes a pod's log, once the pod is gone from the API
// server. A newer pod of the same name keeps its own log.
func (a *Agent) forgetLog(namespace, name, uid string) {
	path := filepath.Join(a.config.StateDir, logsDir, uid+logSuffix)
	a.mu.Lock()
	if a.logs[podKey(namespace, name)] == path {
		delete(a.logs, podKey(namespace, name))
	}
	a.mu.Unlock()
	os.Remove(path)
}

// KubeletHandler answers what the API server asks a kubelet. Of that,
// the agent has logs.
func (a *Agent) KubeletHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(pathContainerLogs, a.serveLogs)
	return mux
}

// serveLogs writes a pod's log: all of it or its last lines, and then,
// for a request that follows, whatever is written until the client
// goes.
func (a *Agent) serveLogs(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	path, has := a.logs[podKey(r.PathValue("namespace"), r.PathValue("pod"))]
	a.mu.Unlock()
	if !has {
		http.Error(w, "no log for the pod", http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "no log for the pod", http.StatusNotFound)
		return
	}
	defer file.Close()

	// What is there now, from the start or from the last lines.
	contents, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	start := 0
	if lines, err := strconv.Atoi(r.URL.Query().Get(queryTailLines)); err == nil && lines >= 0 {
		start = lastLines(contents, lines)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(contents[start:])
	if r.URL.Query().Get(queryFollow) != "true" {
		return
	}

	// What comes after. The file is read from where the first read
	// ended, so nothing is sent twice.
	flusher, _ := w.(http.Flusher)
	ticker := time.NewTicker(followEvery)
	defer ticker.Stop()
	for {
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-ticker.C:
			if _, err := io.Copy(w, file); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		}
	}
}

// lastLines returns where the last `lines` lines of a text start. A
// newline at the very end closes the last line and does not start
// another.
func lastLines(text []byte, lines int) int {
	if lines == 0 {
		return len(text)
	}
	end := len(text)
	if end > 0 && text[end-1] == '\n' {
		end--
	}
	for ; lines > 0; lines-- {
		newline := bytes.LastIndexByte(text[:end], '\n')
		if newline < 0 {
			return 0
		}
		end = newline
	}
	return end + 1
}
