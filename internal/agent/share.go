package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	// it, and mirror the address of each file a mirror was seen to
	// serve byte for byte.
	mu         sync.Mutex
	compressed map[string][]byte
	mirror     map[string]string
}

// mirrored are the shared files a mirror may serve in the agent's
// place, as a container's page names them, each with its name on the
// mirror. The kernel alone: the kernel is nine tenths of what a tab
// downloads, and a page may fetch a kernel from anywhere, which is not
// so of the scripts beside it (a worker's script has to come from the
// page's own origin).
var mirrored = map[string]string{KernelFile: "unkernel.wasm"}

// KernelFile is the kernel a one-CPU guest boots, as a container's
// page names it.
const KernelFile = "unkernel/unkernel.wasm"

// versionFile is the file of the share that names unkernel's version,
// and versionBlank where a mirror's address has that version put in.
const (
	versionFile  = "unkernel/VERSION"
	versionBlank = "{version}"
)

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
	return &Share{dir: dir, version: version, compressed: map[string][]byte{}, mirror: map[string]string{}}, nil
}

// UseMirror sends tabs to a mirror for the files a mirror may serve,
// once the mirror has been seen to serve the share's own bytes. `base`
// is the address of a directory that holds unkernel's published files,
// with {version} where unkernel's version goes. A mirror that cannot
// be reached, or serves other bytes, is an error, and tabs keep
// getting the agent's own files.
func (s *Share) UseMirror(ctx context.Context, client *http.Client, base string) error {
	version, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(versionFile)))
	if err != nil {
		return err
	}
	base = strings.TrimRight(strings.ReplaceAll(base, versionBlank, strings.TrimSpace(string(version))), "/")

	for name, remote := range mirrored {
		// The share's own file, as a hash.
		own, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		want := sha256.Sum256(own)

		// The mirror's, as a hash.
		address := base + "/" + remote
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		got := sha256.New()
		_, err = io.Copy(got, response.Body)
		response.Body.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", address, err)
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: %s", address, response.Status)
		}
		if !bytes.Equal(got.Sum(nil), want[:]) {
			return fmt.Errorf("%s is not the share's %s", address, name)
		}

		s.mu.Lock()
		s.mirror[name] = address
		s.mu.Unlock()
	}
	return nil
}

// Location returns where a tab is sent for a shared file, named as a
// container's page names it: the mirror, for a file the mirror was seen
// to serve, and the agent's own address for every other.
func (s *Share) Location(name string) string {
	s.mu.Lock()
	address, has := s.mirror[name]
	s.mu.Unlock()
	if has {
		return address
	}
	return s.Path(name)
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
