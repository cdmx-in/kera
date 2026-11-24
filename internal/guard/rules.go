// Package guard checks what goes to and comes back from model servers:
// personal data (redacted or blocked), prompt-injection phrases, an admin
// blocklist and, optionally, a moderation model. It wraps provider.Client so
// every outbound call (chat, tasks, compaction, memory, embeddings) is covered.
package guard

import (
	"regexp"
	"strings"
)

// rule is one detector: Re finds candidates, Verify (optional) rejects false
// positives, Mask replaces a hit when redacting.
type rule struct {
	ID     string
	Re     *regexp.Regexp
	Verify func(string) bool
	Mask   string
}

// piiRules in application order: the more specific formats first so a card
// number is masked as a card, not as a phone.
var piiRules = []rule{
	{"pii.email", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), nil, "[email]"},
	{"pii.iban", regexp.MustCompile(`\b[A-Za-z]{2}\d{2}(?:[A-Za-z0-9]{11,30}|(?:-[A-Za-z0-9]{4}){2,7}(?:-[A-Za-z0-9]{1,4})?|(?: [A-Za-z0-9]{4}){2,7}(?: [A-Za-z0-9]{1,4})?)\b`), ibanOK, "[iban]"},
	{"pii.card", regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`), cardOK, "[card]"},
	{"pii.aadhaar", regexp.MustCompile(`\b[2-9]\d{3}[ -]?\d{4}[ -]?\d{4}(?:[ -]?\d+)*\b`), aadhaarOK, "[aadhaar]"},
	{"pii.ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), nil, "[ssn]"},
	{"pii.pan", regexp.MustCompile(`\b[A-Z]{5}\d{4}[A-Z]\b`), nil, "[pan]"},
	// ponytail: phone formats are a heuristic (international +cc, US dashed or
	// bracketed, trunk-0 national, Indian 10-digit mobile); extend the
	// alternation when a deployment's numbers slip through.
	{"pii.phone", regexp.MustCompile(`(?:\+\d{1,3}[ -]?\d(?:[ -]?\d){8,11}|\(\d{3}\)[ -]?\d{3}-\d{4}|\b\d{3}-\d{3}-\d{4}|\b0\d{9,10}|\b[6-9]\d{9})\b`), phoneOK, "[phone]"},
}

// injectionRules match phrases that try to override the system prompt.
var injectionRules = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bignore (?:all |any |the |every )?(?:previous|prior|above|earlier|preceding) (?:instructions|rules|prompts?|messages?)`),
	regexp.MustCompile(`(?i)\bdisregard (?:your|the|all|any) (?:system |previous |prior )?(?:prompt|instructions|rules|guidelines)`),
	regexp.MustCompile(`(?i)\b(?:reveal|print|repeat|show|output|leak|dump) (?:me )?(?:the |your )?(?:system|hidden|initial|secret) (?:prompt|instructions|message)`),
	regexp.MustCompile(`(?i)\byou are now (?:in )?(?:developer|god|unrestricted|dan) mode\b`),
	regexp.MustCompile(`(?i)\bjailbreak(?:ed|ing)?\b`),
	regexp.MustCompile(`(?i)\bDAN mode\b`),
	regexp.MustCompile(`(?i)\bpretend (?:that )?you (?:are|have) no (?:restrictions|rules|guidelines|filters|limits)`),
	regexp.MustCompile(`(?i)\bfrom now on,? (?:you are|act as|respond as) `),
}

func digits(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func luhn(d string) bool {
	sum, double := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

func cardOK(s string) bool {
	d := digits(s)
	return len(d) >= 13 && len(d) <= 19 && luhn(d)
}

// aadhaarOK: 12 digits with a valid Verhoeff check digit (bare 12-digit ids
// such as transaction numbers fail it nine times out of ten).
func aadhaarOK(s string) bool {
	d := digits(s)
	return len(d) == 12 && verhoeff(d)
}

var (
	verhoeffD = [10][10]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 0, 6, 7, 8, 9, 5}, {2, 3, 4, 0, 1, 7, 8, 9, 5, 6}, {3, 4, 0, 1, 2, 8, 9, 5, 6, 7},
		{4, 0, 1, 2, 3, 9, 5, 6, 7, 8}, {5, 9, 8, 7, 6, 0, 4, 3, 2, 1}, {6, 5, 9, 8, 7, 1, 0, 4, 3, 2}, {7, 6, 5, 9, 8, 2, 1, 0, 4, 3},
		{8, 7, 6, 5, 9, 3, 2, 1, 0, 4}, {9, 8, 7, 6, 5, 4, 3, 2, 1, 0}}
	verhoeffP = [8][10]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 5, 7, 6, 2, 8, 3, 0, 9, 4}, {5, 8, 0, 3, 7, 9, 6, 1, 4, 2}, {8, 9, 1, 6, 0, 4, 3, 5, 2, 7},
		{9, 4, 5, 3, 1, 2, 6, 8, 7, 0}, {4, 2, 8, 6, 5, 7, 3, 9, 0, 1}, {2, 7, 9, 3, 8, 0, 6, 4, 1, 5}, {7, 0, 4, 6, 9, 1, 3, 2, 5, 8}}
)

func verhoeff(d string) bool {
	c := 0
	for i := 0; i < len(d); i++ {
		n := int(d[len(d)-1-i] - '0')
		c = verhoeffD[c][verhoeffP[i%8][n]]
	}
	return c == 0
}

func phoneOK(s string) bool {
	n := len(digits(s))
	return n >= 10 && n <= 13
}

// ibanOK is the ISO 13616 mod-97 check.
func ibanOK(s string) bool {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '-' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	cleaned := b.String()
	if len(cleaned) < 15 || len(cleaned) > 34 {
		return false
	}
	r := cleaned[4:] + cleaned[:4]
	rem := 0
	for _, c := range r {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 10
		default:
			return false
		}
		if v >= 10 {
			rem = (rem*10 + v/10) % 97
			rem = (rem*10 + v%10) % 97
		} else {
			rem = (rem*10 + v) % 97
		}
	}
	return rem == 1
}

// Scan masks personal data in text and counts hits per rule.
func Scan(text string) (string, map[string]int) {
	hits := map[string]int{}
	for _, r := range piiRules {
		text = r.Re.ReplaceAllStringFunc(text, func(m string) string {
			if r.Verify != nil && !r.Verify(m) {
				return m
			}
			hits[r.ID]++
			return r.Mask
		})
	}
	return text, hits
}

// ScanInjection counts prompt-injection phrases in text (one rule id).
func ScanInjection(text string) int {
	n := 0
	for _, re := range injectionRules {
		n += len(re.FindAllStringIndex(text, -1))
	}
	return n
}

// SafeCut moves cut left so no PII candidate straddles it: a match that ends
// at or after cut may still grow with the next delta, so it stays unscanned.
func SafeCut(text string, cut int) int {
	for _, r := range piiRules {
		for _, m := range r.Re.FindAllStringIndex(text, -1) {
			if m[1] > cut && m[0] < cut {
				cut = m[0]
			}
		}
	}
	return cut
}
