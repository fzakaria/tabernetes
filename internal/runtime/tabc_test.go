package runtime

import (
	"reflect"
	"testing"
)

// TestLinkPattern checks that the address tabc prints is found on its
// own line, indented and with a carriage return already trimmed, and
// that tabc's other lines and addresses elsewhere are not taken for it.
func TestLinkPattern(t *testing.T) {
	match := linkPattern.FindStringSubmatch("  http://127.0.0.1:33347/t/f5dae7d3fe96c539084647702b6415c7/")
	if match == nil || match[1] != "http://127.0.0.1:33347/t/f5dae7d3fe96c539084647702b6415c7/" {
		t.Errorf("the address was not found: %q", match)
	}
	for _, line := range []string{
		"tabc: open this page to start the container:",
		"  http://example.com/t/abc/",
		"listening on http://127.0.0.1:8000/t/abc/ now",
		"",
	} {
		if linkPattern.MatchString(line) {
			t.Errorf("%q was taken for the address", line)
		}
	}
}

// TestParseList checks that each container of `tabc list` is found by
// its name with its id and status, and that the header, a blank line
// and a line cut short are passed over.
func TestParseList(t *testing.T) {
	output := []byte("ID            NAME   PID      STATUS   CREATED               BUNDLE\n" +
		"22e413756974  c-one  3180832  running  2026-11-26T23:50:01Z  -\n" +
		"9a0b1c2d3e4f  c-two  0        stopped  2026-11-26T23:51:07Z  -\n" +
		"\n" +
		"broken line\n")
	want := map[string]Listed{
		"c-one": {ID: "22e413756974", Status: "running"},
		"c-two": {ID: "9a0b1c2d3e4f", Status: "stopped"},
	}
	if got := parseList(output); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestParseVersion checks that tabc's own version is read from the
// first line of `tabc --version`, and that output without one says so.
func TestParseVersion(t *testing.T) {
	if got := parseVersion([]byte("tabc version 1.0.0\nspec: 1.3.0\n")); got != "1.0.0" {
		t.Errorf("got %q", got)
	}
	if got := parseVersion([]byte("something else\n")); got != "unknown" {
		t.Errorf("got %q", got)
	}
}

// TestLastLine checks that a command's error is the last line it wrote
// that says something, without tabc's prefix or a carriage return.
func TestLastLine(t *testing.T) {
	cases := map[string]string{
		"pulling...\r\ntabc: image \"nope\": not found\r\n\r\n": `image "nope": not found`,
		"one line":                 "one line",
		"":                         "",
		"tabc: usage: tabc pull\n": "usage: tabc pull",
	}
	for output, want := range cases {
		if got := lastLine([]byte(output)); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", output, got, want)
		}
	}
}
