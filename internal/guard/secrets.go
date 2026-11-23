package guard

import (
	"regexp"
	"strings"
	"unicode"
)

// secretRule is one credential shape. group is the submatch that is replaced
// (0 = the whole match), so "password: value" keeps its key and loses its value.
type secretRule struct {
	name  string
	re    *regexp.Regexp
	group int
}

// bareValue is one character of a bare value: it stops at quotes, brackets and JSON escapes.
const bareValue = "[^\\s\"'`<>,;|)\\]}\\\\]"

// secretRules in application order: specific shapes first, key/value last.
// ponytail: a port of the regexes our knowledge-base redactor has used for a
// while; they are heuristics, not a scanner. \b and \w are ASCII-only here
// (RE2), narrower than the original's Unicode word class. A shape that slips
// through is added here, with a case in secrets_test.go.
var secretRules = []secretRule{
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), 0},
	{"aws-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), 0},
	{"github-token", regexp.MustCompile(`\b(?:ghp|gho|ghs|ghu|github_pat)_[A-Za-z0-9_]{20,}`), 0},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), 0},
	{"stripe-key", regexp.MustCompile(`\b[sr]k_live_[A-Za-z0-9]{16,}`), 0},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}`), 0},
	{"pipeline-token", regexp.MustCompile(`\bkp-[A-Za-z0-9]{40,}`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), 0},
	{"bearer", regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9_\-.=+/]{24,})`), 1},
	{"url-credentials", regexp.MustCompile(`\b[a-z][a-z0-9+.\-]{1,15}://[^\s/:@'"<>]{1,64}:([^\s/@'"<>]{3,64})@[\w.\-]+`), 1},
	{"url-secret-param", regexp.MustCompile(`(?i)[?&](?:token|access_token|api_?key|key|sig|signature|X-Amz-Signature|X-Amz-Credential|` +
		`X-Amz-Security-Token)=([^&\s"'<>\\]{16,})`), 1},
	{"key-value", regexp.MustCompile(`(?i)\b[\w\-]*(?:password|passwd|passcode|pwd|secret|api[_\-]?key|apikey|token|private[_\-]?key)[\w\-]*` +
		"(?:\\\\*[\"'`*]|&quot;)*" + `\s*(?:[:=]|=>|\bis\b)\s*` + "(?:\\\\*[\"'`]|&quot;)?(" + bareValue + `{8,})`), 1},
}

// key-value only fires on values that look like a credential: it has a digit
// and is not a placeholder, a date or a number, and the key is not a counter or a duration.
var (
	placeholderRe = regexp.MustCompile(`(?i)^(?:\[REDACTED|\*|x{3}|<|\{|\$|%|your|example|changeme|placeholder|dummy|sample|null|none|true|false|` +
		`process\.env|env\(|getenv|os\.environ|config\(|settings\.|req\.|this\.|self\.)`)
	numericRe      = regexp.MustCompile(`^[\d\-:T.Z/+]+$`)
	notSecretKeyRe = regexp.MustCompile(`(?i)^[\w\-]*?(?:fee|expir|ttl|length|type|count|limit)`) // tokenFee, token_expiry, password_length
	ansiRe         = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[@-Z\\-_])`)
)

// keep reports whether a match is a false positive that stays as it is.
func (r secretRule) keep(whole, v string) bool {
	if strings.HasPrefix(v, "[REDACTED") { // already masked: a second pass changes nothing
		return true
	}
	return r.name == "key-value" && (!strings.ContainsAny(v, "0123456789") || placeholderRe.MatchString(v) ||
		numericRe.MatchString(v) || notSecretKeyRe.MatchString(whole))
}

// MaskSecrets replaces credentials in text with [REDACTED:<rule>] and counts
// the replacements per rule (the values are never returned). It strips ANSI
// escapes and control characters first (StripControl), so a coloured line is
// matched like a plain one and the stored text holds nothing a terminal could act on.
func MaskSecrets(text string) (string, map[string]int) {
	text = StripControl(text)
	counts := map[string]int{}
	for _, r := range secretRules {
		matches := r.re.FindAllStringSubmatchIndex(text, -1)
		if matches == nil {
			continue
		}
		var sb strings.Builder
		last := 0
		for _, m := range matches {
			s, e := m[2*r.group], m[2*r.group+1]
			if s < 0 || r.keep(text[m[0]:m[1]], text[s:e]) {
				continue
			}
			sb.WriteString(text[last:s])
			sb.WriteString("[REDACTED:" + r.name + "]")
			last = e
			counts[r.name]++
		}
		sb.WriteString(text[last:])
		text = sb.String()
	}
	return text, counts
}

// StripControl removes ANSI escape sequences and every control character
// except newline and tab (so NUL, bell, carriage return and DEL go too).
func StripControl(text string) string {
	return strings.Map(func(c rune) rune {
		if c == '\n' || c == '\t' || !unicode.IsControl(c) {
			return c
		}
		return -1
	}, ansiRe.ReplaceAllString(text, ""))
}
