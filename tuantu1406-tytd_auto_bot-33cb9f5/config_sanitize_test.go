package tytd_go

import "testing"

// The invisible test inputs are assembled from CODE POINTS on purpose: pasting
// real zero-width/BOM bytes into source trips Go's illegal-BOM rule and is
// unreadable to reviewers. rw returns the single-rune string for a code point.
func rw(r rune) string { return string([]rune{r}) }

func TestStripInvisible(t *testing.T) {
	zwsp, nbsp, bom := rw(0x200B), rw(0x00A0), rw(0xFEFF)
	cases := []struct{ in, want string }{
		{"haianhz25", "haianhz25"},
		{"haianhz25 ", "haianhz25"},            // trailing ascii space
		{" haianhz25", "haianhz25"},            // leading space
		{"haianhz" + zwsp + "25", "haianhz25"}, // interior ZWSP
		{"haianhz25" + nbsp, "haianhz25"},      // trailing NBSP
		{"x" + bom, "x"},                       // trailing BOM/ZWNBSP
		{zwsp + rw(0x200C) + bom, ""},          // only invisible → empty
		{"Nguyễn" + zwsp + "Huệ", "NguyễnHuệ"}, // accents survive, ZWSP removed
	}
	for _, c := range cases {
		if got := stripInvisible(c.in); got != c.want {
			t.Errorf("stripInvisible(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHasInvisible(t *testing.T) {
	if _, _, bad := hasInvisible("clean"); bad {
		t.Fatal("clean reported invisible")
	}
	n, first, bad := hasInvisible("a" + rw(0x200B) + "b" + rw(0x200C))
	if !bad || n != 2 || first != 0x200B {
		t.Fatalf("hasInvisible mixed: n=%d first=%q bad=%v", n, first, bad)
	}
}

func TestParseServerID(t *testing.T) {
	if n, err := parseServerID("710547"); err != nil || n != 710547 {
		t.Fatalf("plain: %d %v", n, err)
	}
	if n, err := parseServerID(" 710547 "); err != nil || n != 710547 {
		t.Fatalf("spaces: %d %v", n, err)
	}
	if n, err := parseServerID("710547" + rw(0x200B)); err != nil || n != 710547 {
		t.Fatalf("trailing ZWSP: %d %v", n, err)
	}
	// Rejected — must NOT silently return 0 the way the old /server_id did.
	for _, bad := range []string{"", "  ", "abc", "71054x", "0", "-5", "71054" + rw(0x200B) + "7x"} {
		n, err := parseServerID(bad)
		if err == nil {
			t.Errorf("parseServerID(%q) accepted, returned %d", bad, n)
		}
	}
}
