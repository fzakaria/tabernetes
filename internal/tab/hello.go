// Package tab is what a browser tab says about itself when it joins the
// cluster, and the names made from what it says.
//
// Everything in a Hello comes from a stranger's browser. Nothing in it
// is trusted: every use passes through a function here that keeps only
// the characters Kubernetes takes.
package tab

import (
	"strings"
)

// Hello is a tab's first message: what device and browser it is.
type Hello struct {
	// Model is the device, such as "Pixel 9", where the browser tells.
	Model string `json:"model"`
	// Platform is the operating system: "Android", "iOS", "macOS".
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platformVersion"`
	// Browser is "Chrome", "Firefox" or "Safari".
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browserVersion"`
	// Threads is how many threads the browser says the device has.
	Threads int `json:"threads"`
	// Touch is whether the device's pointer is a finger.
	Touch bool `json:"touch"`
	// Zone is the browser's time zone, such as "Europe/Lisbon". The site
	// puts a node on its map by the zone's city, which says roughly
	// where a node is without the agent looking up anyone's address.
	Zone string `json:"zone"`
}

// The limits of a name: a DNS label's length, and how much of it the
// device's own words may take, which leaves room for the suffix.
const (
	maxNameLength = 63
	maxBaseLength = 40
)

// maxLabelValueLength is the longest label value Kubernetes takes.
const maxLabelValueLength = 63

// What a node is called when its browser said nothing usable, and what
// a label says then.
const (
	fallbackName  = "tab"
	fallbackLabel = "unknown"
	fallbackImage = "a browser tab"
)

// NodeName returns the node's name: the device's model, or the platform
// and browser where the browser gives no model, then the suffix. The
// suffix is what makes two phones of one model two nodes.
func (h Hello) NodeName(suffix string) string {
	base := slug(h.Model)
	if base == "" {
		base = slug(strings.TrimSpace(h.Platform + " " + h.Browser))
	}
	if base == "" {
		base = fallbackName
	}
	return base + "-" + suffix
}

// Describe returns a line for the node's OS image: the browser and the
// platform, each with its version where the browser sent one.
func (h Hello) Describe() string {
	browser := strings.TrimSpace(printable(h.Browser) + " " + printable(h.BrowserVersion))
	platform := strings.TrimSpace(printable(h.Platform) + " " + printable(h.PlatformVersion))
	switch {
	case browser != "" && platform != "":
		return browser + " on " + platform
	case browser != "":
		return browser
	case platform != "":
		return platform
	default:
		return fallbackImage
	}
}

// slug returns text as part of a DNS label: lowercase letters and
// digits, with one dash wherever anything else stood between them, cut
// to maxBaseLength.
func slug(text string) string {
	var out strings.Builder
	pendingDash := false
	for _, char := range strings.ToLower(text) {
		isLetter := char >= 'a' && char <= 'z'
		isDigit := char >= '0' && char <= '9'
		if !isLetter && !isDigit {
			pendingDash = out.Len() > 0
			continue
		}
		if out.Len() >= maxBaseLength {
			break
		}
		if pendingDash {
			out.WriteByte('-')
			pendingDash = false
		}
		out.WriteRune(char)
	}
	return strings.TrimRight(out.String(), "-")
}

// LabelValue returns text as a label value: letters, digits, dots and
// underscores, with one dash wherever anything else stood between them,
// or the fallback when nothing is left.
func LabelValue(text string) string {
	var out strings.Builder
	pendingDash := false
	for _, char := range text {
		isLetter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
		isDigit := char >= '0' && char <= '9'
		if !isLetter && !isDigit && char != '.' && char != '_' {
			pendingDash = out.Len() > 0
			continue
		}
		if out.Len() >= maxLabelValueLength-1 {
			break
		}
		if pendingDash {
			out.WriteByte('-')
			pendingDash = false
		}
		out.WriteRune(char)
	}
	value := strings.Trim(out.String(), "-._")
	if value == "" {
		return fallbackLabel
	}
	return value
}

// maxPrintableLength is how much of a browser's word goes into a line
// people read.
const maxPrintableLength = 32

// printable returns text with only the characters a version or a
// product name is made of, cut to maxPrintableLength.
func printable(text string) string {
	var out strings.Builder
	for _, char := range strings.TrimSpace(text) {
		isLetter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
		isDigit := char >= '0' && char <= '9'
		if !isLetter && !isDigit && char != '.' && char != ' ' && char != '-' {
			continue
		}
		if out.Len() >= maxPrintableLength {
			break
		}
		out.WriteRune(char)
	}
	return strings.TrimSpace(out.String())
}

// The limits of what a browser says besides its names: the longest a
// time zone's name is taken, and the most threads a device is believed
// to have.
const (
	maxZoneLength = 64
	maxThreads    = 1024
)

// PlainZone returns the tab's time zone if the name is made of what
// zone names are made of: letters, digits, and the marks between them,
// with a letter first and no dots. Anything else is no zone.
func (h Hello) PlainZone() string {
	zone := h.Zone
	if zone == "" || len(zone) > maxZoneLength {
		return ""
	}
	for index, char := range zone {
		isLetter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
		isDigit := char >= '0' && char <= '9'
		isMark := char == '/' || char == '_' || char == '-' || char == '+'
		if index == 0 && !isLetter {
			return ""
		}
		if !isLetter && !isDigit && !isMark {
			return ""
		}
	}
	return zone
}

// PlainThreads returns how many threads the tab's device has, or none
// when the browser's number is not one a device could have.
func (h Hello) PlainThreads() int {
	if h.Threads < 0 || h.Threads > maxThreads {
		return 0
	}
	return h.Threads
}
