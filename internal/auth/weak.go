package auth

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode"
)

// Beyond the length rules a password may not be one of the few thousand things attackers
// try first. The list is small on purpose: whole passwords (exact), and base words that
// stay weak with digits or symbols around them ("welcome2024!"); common_passwords.json is
// copied byte for byte to web/src/lib so the sign-in forms say the same, and a test keeps
// the two equal. Nothing here is a substitute for a breach list.
//
//go:embed common_passwords.json
var commonJSON []byte

var common = sync.OnceValue(func() (c struct{ exact, words map[string]bool }) {
	var raw struct{ Exact, Words []string }
	if err := json.Unmarshal(commonJSON, &raw); err != nil {
		panic("common_passwords.json: " + err.Error())
	}
	c.exact, c.words = map[string]bool{}, map[string]bool{}
	for _, w := range raw.Exact {
		c.exact[w] = true
	}
	for _, w := range raw.Words {
		c.words[w] = true
	}
	return c
})

var (
	ErrPasswordCommon  = errors.New("That password is too common. Choose something longer or less usual.")
	ErrPasswordPattern = errors.New("Avoid repeated characters and simple runs like 1234567890 or abcdefghij.")
	ErrPasswordContext = errors.New("Do not build the password from your email name or the word kera.")
)

// weakness returns the rule a password breaks beyond length, or nil. email may be empty.
func weakness(password, email string) error {
	lower := strings.ToLower(password)
	c := common()
	// The core is the password without the digits and symbols around it: "p@ssw0rd2024!" -> "p@ssw0rd".
	core := strings.TrimFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
	switch {
	case onlyContext(lower, email):
		return ErrPasswordContext
	case c.exact[lower] || c.words[letters(lower)] || c.words[letters(unleet(core))]:
		return ErrPasswordCommon
	case repeated(lower) || inRun(lower):
		return ErrPasswordPattern
	}
	return nil
}

// letters keeps the letters of s: "welcome2024!" becomes "welcome".
func letters(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, s)
}

// unleet undoes the common look-alike swaps, so "p@ssw0rd" reads as "password".
var unleet = strings.NewReplacer("@", "a", "$", "s", "0", "o", "1", "l", "3", "e", "4", "a", "5", "s", "7", "t", "!", "i").Replace

// repeated: one character, or a block of up to four that repeats ("abababababab", "123123123123").
func repeated(s string) bool {
	r := []rune(s)
	for p := 1; p <= 4 && p < len(r); p++ {
		same := true
		for i := p; i < len(r) && same; i++ {
			same = r[i] == r[i-p]
		}
		if same {
			return true
		}
	}
	return false
}

// runs are strings a password may be a stretch of, forwards or backwards: digits (wrapping 9 to 0),
// the alphabet, and the rows of a keyboard.
var runs = func() []string {
	base := []string{"01234567890123456789", "abcdefghijklmnopqrstuvwxyz", "qwertyuiop", "asdfghjkl", "zxcvbnm",
		"qwertyuiopasdfghjklzxcvbnm", "1234567890qwertyuiopasdfghjklzxcvbnm"}
	out := append([]string{}, base...)
	for _, b := range base {
		r := []rune(b)
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
		out = append(out, string(r))
	}
	return out
}()

func inRun(s string) bool {
	for _, r := range runs {
		if strings.Contains(r, s) {
			return true
		}
	}
	return false
}

// onlyContext: nothing is left but digits and symbols once the word kera and the parts of the
// email name are taken out ("kera123456", "ann.lee -> annlee2024!").
func onlyContext(lower, email string) bool {
	words := []string{"kera"}
	if local, _, ok := strings.Cut(strings.ToLower(email), "@"); ok {
		if l := letters(local); len(l) >= 4 {
			words = append(words, l)
		}
		for _, part := range strings.FieldsFunc(local, func(r rune) bool { return !unicode.IsLetter(r) }) {
			if len(part) >= 4 {
				words = append(words, part)
			}
		}
	}
	rest, removed := lower, false
	for _, w := range words {
		if strings.Contains(rest, w) {
			rest, removed = strings.ReplaceAll(rest, w, ""), true
		}
	}
	return removed && letters(rest) == ""
}
