package oauthclient

import "testing"

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/?tools=x": "/?tools=x", "/c/1": "/c/1", "": "/d", "//evil.example": "/d", "https://evil.example/": "/d",
		"/\\evil.example": "/d", "/a\nb": "/d", "relative": "/d",
	} {
		if got := safeNext(in, "/d"); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
