package model

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The login push carries the in-game nickname as free-form UTF-8: Vietnamese
// with combining and precomposed marks, and whatever decorative symbols the
// player pasted in (✢★ etc.). Players can also smuggle INVISIBLE characters —
// zero-width joins, bidi controls, the BOM, tag characters — that spoof page
// and chat UI (an RTL control can render "ban ⅄" as "␡ nauq", hiding who a
// name really claims to be). Anything we persist from that wire is run
// through SanitizeIngameName first, so stored bytes never disagree with
// what a browser or terminal actually paints.
//
// Deliberate trade: invisible format characters are dropped wholesale. A
// name built around emoji ZWJ sequences loses its join and shows the two
// emoji apart — cosmetic only, and rarer than the spoofing it prevents.
// Visible marks (N, M categories), variation selectors, and every real
// letter/digit/symbol survive untouched.

// MaxIngameNameRunes bounds the stored name; the game itself caps nicknames
// far below this, so only abusive pushes are affected.
const MaxIngameNameRunes = 64

func SanitizeIngameName(raw string) string {
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "") // never persist invalid sequences
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		// C0/C1 controls and the whole Cf ("format") category: zero-width
		// spaces/joins/marks, LTR/RTL overrides and isolates, BOM, soft
		// hyphen, invisible times/function-application, tag chars.
		// Cc covers all C0 and C1 controls (incl. U+0085), Cf every format
		// char the game's name field can abuse.
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String()) // TrimSpace is unicode-aware (U+3000 too)
	if utf8.RuneCountInString(s) > MaxIngameNameRunes {
		s = strings.TrimSpace(string([]rune(s)[:MaxIngameNameRunes]))
	}
	return s
}
