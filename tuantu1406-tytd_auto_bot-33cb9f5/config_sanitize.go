package tytd_go

// Credential-input hardening (owner audit 2026-10-10: "user config server id
// thừa 1 dấu space hay ki tu ko nhin thay"). Game login fails with a bare
// "login failed" when a username/password/server-id was pasted from chat or a
// web page carrying an invisible character — none of which strings.Fields or
// strings.TrimSpace removes, because they are NOT unicode.IsSpace. The worst
// offender was /server_id ignoring strconv's error and silently writing 0, so
// every later login used server 0. Everything that writes these fields now
// routes through stripInvisible / parseServerID so the bad byte is either
// cleaned or the save is rejected with the offending token shown %q-visible.
//
// The rune table is built from CODE POINTS (hex ints) rather than literals on
// purpose: a U+FEFF rune literal is an illegal BOM and zero-width literals are
// invisible to reviewers, so the integers are the only honest, readable form.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// invisibleRunes are zero-width, bidi-mark and copy-paste space artifacts. A
// real game login username/password never needs them; a stray one is always an
// artifact, so stripping is safe and kills the "looks correct but login fails"
// class of bugs.
var invisibleRunes = buildInvisibleRunes()

func buildInvisibleRunes() map[rune]bool {
	m := make(map[rune]bool)
	for _, r := range []rune{
		0x200B, // zero width space
		0x200C, // zero width non-joiner
		0x200D, // zero width joiner
		0xFEFF, // BOM / zero width no-break space
		0x2060, // word joiner
		0x00A0, // non-breaking space
		0x2028, // line separator
		0x2029, // paragraph separator
		0x200E, // left-to-right mark
		0x200F, // right-to-left mark
	} {
		m[r] = true
	}
	return m
}

// stripInvisible removes invisibleRunes anywhere in s and trims every kind of
// surrounding unicode whitespace. Game credentials and server ids never need
// an interior zero-width char, so this cannot lose a legitimate value — it can
// only repair a copy-paste artifact.
func stripInvisible(s string) string {
	if s == "" {
		return s
	}
	needs := false
	for _, r := range s {
		if invisibleRunes[r] {
			needs = true
			break
		}
	}
	if needs {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			if !invisibleRunes[r] {
				b.WriteRune(r)
			}
		}
		s = b.String()
	}
	return strings.TrimSpace(s)
}

// hasInvisible reports the count and first offending rune of s, for
// /check_config to tell the owner a stored value is silently corrupted.
func hasInvisible(s string) (int, rune, bool) {
	n := 0
	var first rune
	for _, r := range s {
		if invisibleRunes[r] {
			if n == 0 {
				first = r
			}
			n++
		}
	}
	return n, first, n > 0
}

// parseServerID turns a form/Telegram token into a validated positive server
// id. Unlike the old /server_id code it NEVER swallows the error into a 0: a
// token that is not pure digits after cleaning is rejected, and the caller is
// shown the raw token %q so a hidden byte is visible. The numeric guard (<=0)
// matches the web forms' own requirement.
func parseServerID(raw string) (int, error) {
	s := stripInvisible(raw)
	if s == "" {
		return 0, fmt.Errorf("trống")
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return 0, fmt.Errorf("%q không phải số thuần (có thể còn kí tự ẩn)", raw)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q không parse được: %v", raw, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("server_id phải > 0 (nhận %q)", raw)
	}
	return n, nil
}

// serverErrHint renders an optional trailing " (reason)" for the web flash
// lines that already have their own generic message, so the %q detail from
// parseServerID survives to the user without changing the happy-path text.
func serverErrHint(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

// configCredLine renders one stored credential %q-quoted (so any invisible
// byte shows as ​ etc.) and appends a warning when invisible runes are
// present. Used by /check_config to make "looks right but login fails" visible.
func configCredLine(label, val string) string {
	n, first, bad := hasInvisible(val)
	if bad {
		return fmt.Sprintf("%s = %q  ⚠️ %d kí tự ẩn (vd \\u%04X)", label, val, n, first)
	}
	return fmt.Sprintf("%s = %q", label, val)
}

// configServerLine flags the silent-server-0 case explicitly, the exact
// failure the owner suspected.
func configServerLine(serverID int) string {
	if serverID <= 0 {
		return fmt.Sprintf("server_id = %d  ⚠️ vô hiệu (0/không đặt) → login sẽ thất bại; đặt lại bằng /server_id <số>", serverID)
	}
	return fmt.Sprintf("server_id = %d", serverID)
}
