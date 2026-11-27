package agent

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// versionLength is how much of the files' hash goes in their address.
const versionLength = 12

// How long a browser may keep a shared file: a year, and without asking
// again, since a changed file has a changed address.
const cacheForever = "public, max-age=31536000, immutable"

// The types of the files a browser will only take under their own type.
var contentTypes = map[string]string{
	".js":   "text/javascript",
	".mjs":  "text/javascript",
	".wasm": "application/wasm",
	".css":  "text/css",
}

// Share serves the files of tabc's that every container's page loads
// alike, under an address that changes when any of them does, so a
// browser fetches the kernel once and keeps it.
type Share struct {
	dir     string
	version string

	// compressed holds each file gzipped, from the first request for
	// it.
	mu         sync.Mutex
	compressed map[string][]byte
}

// OpenShare returns the files under a directory, which is tabc's share:
// the directory tabc's own build embeds.
func OpenShare(dir string) (*Share, error) {
	// The version is a hash of every shared file's name and bytes.
	hash := sha256.New()
	for _, shared := range sharedDirs {
		err := filepath.WalkDir(filepath.Join(dir, shared), func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			contents, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			hash.Write([]byte(name))
			hash.Write(contents)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	version := hex.EncodeToString(hash.Sum(nil))[:versionLength]
	return &Share{dir: dir, version: version, compressed: map[string][]byte{}}, nil
}

// Path returns the address of a shared file, named as a container's
// page names it: "unkernel/web.mjs".
func (s *Share) Path(name string) string {
	return pathStatic + s.version + "/" + name
}

// ServeHTTP serves one shared file, gzipped for a browser that takes
// gzip.
func (s *Share) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, pathStatic+s.version+"/")
	if !ok || !filepath.IsLocal(name) || !isShared(name) {
		http.NotFound(w, r)
		return
	}

	header := w.Header()
	header.Set("Cache-Control", cacheForever)
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Vary", "Accept-Encoding")
	contentType := contentTypes[path.Ext(name)]
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(name))
	}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}

	file := filepath.Join(s.dir, filepath.FromSlash(name))
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		http.ServeFile(w, r, file)
		return
	}
	body, err := s.gzipped(name, file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	header.Set("Content-Encoding", "gzip")
	w.Write(body)
}

// gzipped returns a file compressed, compressing it the first time.
func (s *Share) gzipped(name, file string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if body, has := s.compressed[name]; has {
		return body, nil
	}

	contents, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(contents); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	s.compressed[name] = out.Bytes()
	return out.Bytes(), nil
}

// isShared reports whether a file is in one of the shared directories.
func isShared(name string) bool {
	for _, dir := range sharedDirs {
		if strings.HasPrefix(name, dir) {
			return true
		}
	}
	return false
}
