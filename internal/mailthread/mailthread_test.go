package mailthread

import "testing"

func TestStripBrackets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<content@example.com>", "content@example.com"},
		{"no brackets", "no brackets"},
		{"<only left>", "only left"}, // both ends present -> stripped
		{"<>", ""},
		{"  <spaced@example.com>  ", "spaced@example.com"},
		{"<id", "<id"},   // stray single bracket -> left intact
		{"id>", "id>"},   // stray single bracket -> left intact
		{"", ""},
	}
	for _, c := range cases {
		if got := StripBrackets(c.in); got != c.want {
			t.Errorf("StripBrackets(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRootPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		own, irt    string
		refs        []string
		wantRoot    string
		wantIsRoot  bool
	}{
		{"references win (last entry)", "<own@x>", "<irt@x>", []string{"<a@x>", "<b@x>"}, "b@x", false},
		{"in-reply-to when no references", "<own@x>", "<irt@x>", nil, "irt@x", false},
		{"own message-id roots the thread", "<own@x>", "", nil, "own@x", true},
		{"empty references slice falls through to irt", "<own@x>", "<irt@x>", []string{}, "irt@x", false},
		{"blank last reference falls through", "<own@x>", "<irt@x>", []string{"  "}, "irt@x", false},
		{"nothing usable -> empty root, isRoot true", "", "", nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, isRoot := Root(c.own, c.irt, c.refs)
			if root != c.wantRoot || isRoot != c.wantIsRoot {
				t.Errorf("Root(%q,%q,%v) = (%q,%v), want (%q,%v)",
					c.own, c.irt, c.refs, root, isRoot, c.wantRoot, c.wantIsRoot)
			}
		})
	}
}
