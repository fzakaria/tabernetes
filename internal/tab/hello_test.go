package tab

import (
	"strings"
	"testing"
)

// TestNodeName checks that a node is named after the device when the
// browser gives a model, after the platform and browser when it gives
// none, and "tab" when it gives nothing, each with the suffix after it.
func TestNodeName(t *testing.T) {
	cases := []struct {
		hello Hello
		want  string
	}{
		{Hello{Model: "Pixel 9 Pro", Platform: "Android", Browser: "Chrome"}, "pixel-9-pro-3fa2"},
		{Hello{Platform: "iOS", Browser: "Safari"}, "ios-safari-3fa2"},
		{Hello{Platform: "macOS", Browser: "Chrome"}, "macos-chrome-3fa2"},
		{Hello{Browser: "Firefox"}, "firefox-3fa2"},
		{Hello{}, "tab-3fa2"},
	}
	for _, c := range cases {
		if got := c.hello.NodeName("3fa2"); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.hello, got, c.want)
		}
	}
}

// TestPlainSuffix checks that a tab gets the end of its name back only
// when what it asks for is what the agent would have made: four
// characters, each a digit or a lowercase letter up to f. Anything
// else, which may be anything a stranger typed, is no suffix.
func TestPlainSuffix(t *testing.T) {
	if got := (Hello{Suffix: "3fa2"}).PlainSuffix(); got != "3fa2" {
		t.Errorf("a suffix the agent could have made came back as %q", got)
	}
	for _, hostile := range []string{"", "3fa", "3fa2b", "3FA2", "3f.2", "../x", "zzzz"} {
		if got := (Hello{Suffix: hostile}).PlainSuffix(); got != "" {
			t.Errorf("%q was taken as the suffix %q", hostile, got)
		}
	}
}

// TestNodeNameIsALabel checks that whatever a browser sends, the name is
// one Kubernetes takes: lowercase letters, digits and dashes, starting
// and ending with a letter or digit, and no longer than a DNS label.
func TestNodeNameIsALabel(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"<script>alert(1)</script>",
		"--leading and trailing--",
		"ÜBER phone™",
		strings.Repeat("long", 100),
		"   ",
	}
	for _, model := range hostile {
		name := Hello{Model: model}.NodeName("3fa2")
		if len(name) > maxNameLength {
			t.Errorf("%q: name %q is %d long", model, name, len(name))
		}
		for _, char := range name {
			if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				t.Errorf("%q: name %q has %q in it", model, name, char)
			}
		}
		if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
			t.Errorf("%q: name %q has a dash out of place", model, name)
		}
	}
}

// TestDescribe checks the line a node's OS image is given: the browser
// and the platform with their versions, and only the parts the browser
// sent.
func TestDescribe(t *testing.T) {
	cases := []struct {
		hello Hello
		want  string
	}{
		{Hello{Browser: "Chrome", BrowserVersion: "142", Platform: "Android", PlatformVersion: "16"}, "Chrome 142 on Android 16"},
		{Hello{Browser: "Safari", Platform: "iOS"}, "Safari on iOS"},
		{Hello{Browser: "Firefox", BrowserVersion: "153"}, "Firefox 153"},
		{Hello{}, "a browser tab"},
	}
	for _, c := range cases {
		if got := c.hello.Describe(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.hello, got, c.want)
		}
	}
}

// TestLabelValue checks that text from a browser becomes a label value
// Kubernetes takes, and that text with nothing usable in it becomes the
// fallback.
func TestLabelValue(t *testing.T) {
	cases := map[string]string{
		"Pixel 9 Pro":       "Pixel-9-Pro",
		"Chrome":            "Chrome",
		"  ":                "unknown",
		"<b>":               "b",
		"a/b\\c":            "a-b-c",
		"-x-":               "x",
		"":                  "unknown",
		"iPhone15,2 (weird": "iPhone15-2-weird",
	}
	for text, want := range cases {
		if got := LabelValue(text); got != want {
			t.Errorf("LabelValue(%q) = %q, want %q", text, got, want)
		}
	}
}

// TestZone checks that a time zone's name is kept when it is made of
// what such names are made of, and dropped when it is anything else: a
// zone is a stranger's text that ends up on a page.
func TestZone(t *testing.T) {
	kept := []string{"America/Los_Angeles", "Asia/Kolkata", "Etc/GMT+8", "UTC", "America/Argentina/Buenos_Aires"}
	for _, zone := range kept {
		if got := (Hello{Zone: zone}).PlainZone(); got != zone {
			t.Errorf("%q became %q", zone, got)
		}
	}
	dropped := []string{"", "<script>", "America/Los Angeles", "../../etc", strings.Repeat("A", 100), "12345", "a;b"}
	for _, zone := range dropped {
		if got := (Hello{Zone: zone}).PlainZone(); got != "" {
			t.Errorf("%q was kept as %q", zone, got)
		}
	}
}

// TestPlainThreads checks that a thread count is kept when it is one a
// device could have, and counted as none otherwise.
func TestPlainThreads(t *testing.T) {
	cases := map[int]int{8: 8, 1: 1, 0: 0, -4: 0, 100000: 0}
	for threads, want := range cases {
		if got := (Hello{Threads: threads}).PlainThreads(); got != want {
			t.Errorf("%d threads became %d, want %d", threads, got, want)
		}
	}
}
