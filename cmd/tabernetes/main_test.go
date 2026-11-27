package main

import (
	"reflect"
	"testing"

	"github.com/fzakaria/tabernetes/internal/agent"
)

// TestParseExposed checks the flag that names the Services a visitor
// may reach: none when empty, each entry read into its parts, the path
// an entry ends with kept as the only one a visitor may ask for, and
// an entry missing a part refused.
func TestParseExposed(t *testing.T) {
	none, err := parseExposed("")
	if err != nil || len(none) != 0 {
		t.Errorf("empty: %v, %v", none, err)
	}

	got, err := parseExposed("counter=default/web:8000, chat=talk/chat:80/rooms/open")
	want := map[string]agent.Exposed{
		"counter": {Namespace: "default", Service: "web", Port: 8000},
		"chat":    {Namespace: "talk", Service: "chat", Port: 80, Path: "/rooms/open"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, %v", got, err)
	}

	for _, text := range []string{"counter", "counter=web:8000", "counter=default/web", "=default/web:8000", "counter=default/web:http", "counter=default/web:0", "counter=default/web:8000/"} {
		if _, err := parseExposed(text); err == nil {
			t.Errorf("%q was taken", text)
		}
	}
}
