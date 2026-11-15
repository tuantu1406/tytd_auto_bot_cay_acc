package model

import (
	"strings"
	"testing"
	"unicode/utf8"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Every INVISIBLE code point in these cases is a Go escape sequence, never
// a literal: a raw zero-width character sitting in a source file is exactly
// the confusion this sanitizer exists to prevent.

func TestSanitizeIngameName(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// Visible decoration and precomposed Vietnamese survive byte-for-byte.
		{"decorated vietnamese passes through", "\u2722\u2605\u2722L\u1ec5nhl\u00fcng", "\u2722\u2605\u2722L\u1ec5nhl\u00fcng"},
		// Decomposed "ệ" (e + U+0302 + U+0323) is kept: combining marks are
		// category Mn, not format characters.
		{"combining marks kept", "L\u0065\u0302\u0323nh", "L\u0065\u0302\u0323nh"},
		// ZWSP, ZWJ, RLO: invisible, dropped.
		{"zwsp zwj rlo stripped", "a\u200bb\u200dc\u202ed", "abcd"},
		// Soft hyphen, invisible separator, BOM.
		{"soft hyphen invisible-sep bom stripped", "p\u00adq\u2060r\ufeff", "pqr"},
		// Unicode tag characters (smuggling channel).
		{"tag chars stripped", "sneaky\U000E0067\U000E0062", "sneaky"},
		// C0 (tab/newline) and C1 (U+0085 NEL) controls.
		{"controls stripped", "a\tb\nc\u0085d", "abcd"},
		// Raw invalid bytes must never reach SQLite.
		{"invalid utf8 dropped", "qu\u1ea3\xff\xfe", "qu\u1ea3"},
		// Ideographic space and NBSP around a plain name.
		{"unicode whitespace trimmed", "\u3000Luffy\u00a0", "Luffy"},
		{"empty stays empty", "\t\u200b ", ""},
	}
	for _, c := range cases {
		if got := SanitizeIngameName(c.in); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}

	// Rune cap: cut on code points, never mid-sequence.
	long := strings.Repeat("\u1ea1", MaxIngameNameRunes+50)
	got := SanitizeIngameName(long)
	if utf8.RuneCountInString(got) != MaxIngameNameRunes || !utf8.ValidString(got) {
		t.Errorf("cap: %d runes, valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
}

func TestIngameNamePersist(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ingame_name_test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}, &EventUser{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&User{ID: "123", Username: "acc1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&EventUser{ID: "8000000001", Username: "ev1"}).Error; err != nil {
		t.Fatal(err)
	}

	// A decorated name with a trailing RLO — the stored value must be clean.
	name := SanitizeIngameName("\u2722\u2605\u2722L\u1ec5nhl\u00fcng\u202e")
	if name != "\u2722\u2605\u2722L\u1ec5nhl\u00fcng" {
		t.Fatalf("sanitize before store: %q", name)
	}
	if err := UpdateUserIngameName(db, "123", name); err != nil {
		t.Fatal(err)
	}
	if err := UpdateEventUserIngameName(db, "8000000001", name); err != nil {
		t.Fatal(err)
	}
	var u User
	if err := db.First(&u, "id = ?", "123").Error; err != nil || u.IngameName != name {
		t.Fatalf("user roundtrip: %q %v", u.IngameName, err)
	}
	var eu EventUser
	if err := db.First(&eu, "id = ?", "8000000001").Error; err != nil || eu.IngameName != name {
		t.Fatalf("event user roundtrip: %q %v", eu.IngameName, err)
	}
	// GetEventUsersByLeader must carry the new column too (page reads it).
	list, err := GetEventUsersByLeader(db, "")
	if err != nil || len(list) != 1 || list[0].IngameName != name {
		t.Fatalf("leader list: %+v %v", list, err)
	}
	// An unknown id updates nothing but is not an error (no retry storm).
	if err := UpdateUserIngameName(db, "nope", name); err != nil {
		t.Fatalf("unknown id should not fail the update call: %v", err)
	}
}
