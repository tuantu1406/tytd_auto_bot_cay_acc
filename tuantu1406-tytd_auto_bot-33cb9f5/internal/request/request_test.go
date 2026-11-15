package request

import (
	"net/url"
	"testing"
)

// Step-4 data must equal what the game client builds with
// encodeURIComponent(JSON.stringify({userId,timeStamp,sign})) after
// decodeURIComponent on the enterGameUrl params. The trap: Go's url.Values
// from url.Query() turns '+' into ' ' (form semantics), while
// decodeURIComponent leaves '+' alone — a base64 sign with a literal '+'
// then reaches player/login corrupted and the server answers an anonymous
// refusal that reads like a wrong password.
func TestRawQueryParamsKeepsPlus(t *testing.T) {
	// sign as it appears in a raw query: '+' NOT percent-encoded, one %3D.
	v := rawQueryParams("uid=710547&timestamp=1760000000&sign=ab+cd/ef%3D%3D")
	if got := v.Get("sign"); got != "ab+cd/ef==" {
		t.Fatalf("sign = %q, want %q", got, "ab+cd/ef==")
	}
	if got := v.Get("uid"); got != "710547" {
		t.Fatalf("uid = %q", got)
	}
	// Contrast: the OLD path corrupted it — guards the regression.
	u, err := url.Parse("https://x/y?sign=ab+cd")
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("sign"); got != "ab cd" {
		t.Fatalf("url.Query() '+'→space reference broken (%q) — re-check this test", got)
	}
}
