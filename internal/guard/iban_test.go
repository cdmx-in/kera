package guard

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The length table is the only thing standing between an id that happens to pass mod 97 and an [iban] mask.
func TestIBANLengthTable(t *testing.T) {
	for cc, n := range ibanLen {
		if len(cc) != 2 || cc != strings.ToUpper(cc) || n < 15 || n > 34 {
			t.Errorf("ibanLen[%q] = %d is not a country code and a length between 15 and 34", cc, n)
		}
	}
	// well-known lengths from the registry's own examples
	for cc, want := range map[string]int{"DE": 22, "GB": 22, "FR": 27, "ES": 24, "IT": 27, "NL": 18, "NO": 15, "BE": 16, "CH": 21, "MT": 31, "LC": 32} {
		if ibanLen[cc] != want {
			t.Errorf("ibanLen[%s] = %d, want %d", cc, ibanLen[cc], want)
		}
	}
}

// Hashes and ids pasted into a chat must not be masked as IBANs. About 1 in 97 random strings passes
// the check digits, and lowercase hex tokens starting with two letters and two digits are all the
// pattern needs, so before the length table roughly 1 in 2,000 lowercase hashes was masked.
func TestHexHashesAreNotIBANs(t *testing.T) {
	rnd := rand.New(rand.NewSource(20261124))
	const hexDigits = "0123456789abcdef"
	for _, size := range []int{32, 40, 64} { // md5, sha1, sha256
		hit := 0
		for i := 0; i < 60000; i++ {
			b := make([]byte, size)
			for j := range b {
				b[j] = hexDigits[rnd.Intn(16)]
			}
			// shape the start like the pattern wants, so nearly every sample is a candidate
			b[0], b[1] = "abcdef"[rnd.Intn(6)], "abcdef"[rnd.Intn(6)]
			b[2], b[3] = "0123456789"[rnd.Intn(10)], "0123456789"[rnd.Intn(10)]
			if detect(fmt.Sprintf("hash %s end", b))["pii.iban"] > 0 {
				hit++
			}
		}
		if hit != 0 {
			t.Errorf("%d of 60000 random %d-character hex hashes were masked as IBANs", hit, size)
		}
	}
}

// A spaced IBAN followed by a short word used to be missed: the pattern takes the word as one more
// group, the whole match fails the check digits, and no shorter match was tried.
func TestSpacedIBANFollowedByShortWords(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Send to ES91 2100 0418 4502 0005 1332 to settle", "Send to [iban] to settle"},
		{"ES91 2100 0418 4502 0005 1332 from Ana", "[iban] from Ana"},
		{"BE68 5390 0754 7034 and more", "[iban] and more"},
		{"AT61 1904 3002 3457 3201 is mine", "[iban] is mine"},
		{"SA03 8000 0000 6080 1016 7519 x", "[iban] x"},
		{"PL61 1090 1014 0000 0712 1981 2874 for rent", "[iban] for rent"},
		{"GB82 WEST 1234 5698 7654 32 confirmed", "[iban] confirmed"},                                // still masked as before
		{"pay ES91 2100 0418 4502 0005 1332 or GB82 WEST 1234 5698 7654 32", "pay [iban] or [iban]"}, // two in one line
		{"pay ES91 2100 0418 4502 0005 1332.", "pay [iban]."},
	} {
		if got, _ := Scan(c.in); got != c.want {
			t.Errorf("Scan(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// a wrong check digit stays unmasked, with or without words after it, and nothing shorter sneaks through
	for _, in := range []string{
		"Reference DE12 3456 7890 1234 5678 90 and then more",
		"ES12 2100 0418 4502 0005 1332 to settle",
		"order AB12 3456 7890 1234 5678 now",
	} {
		if got, hits := Scan(in); hits["pii.iban"] != 0 {
			t.Errorf("Scan(%q) masked an IBAN: %q", in, got)
		}
	}
}
