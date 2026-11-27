// Package web holds the site's own files: the page that shows the
// cluster and the page a browser opens to become a node.
package web

import "embed"

// Files are the site's pages, styles and scripts.
//
//go:embed *.html *.css *.js *.svg
var Files embed.FS
