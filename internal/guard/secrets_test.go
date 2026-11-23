package guard

import (
	"reflect"
	"strings"
	"testing"
)

// The fake credentials are assembled from parts so no scanner mistakes this file for a leak.
var (
	fakeAWS    = "AKIA" + "ABCDEFGH12345678"
	fakeGoogle = "AIza" + strings.Repeat("Ab1_", 8) + "Xyz"
	fakeGitHub = "ghp" + "_" + strings.Repeat("a1B2", 9)
	fakeSlack  = "xoxb" + "-1234567890-abcdefghij"
	fakeStripe = "sk" + "_live_" + strings.Repeat("Q7", 12)
	fakeSK     = "sk" + "-" + strings.Repeat("aB3", 14)
	fakeKP     = "kp" + "-" + strings.Repeat("Zy9", 13) + "Z"
	fakeJWT    = "eyJ" + "hbGciOiJIUzI1NiJ9" + "." + "eyJ" + "zdWIiOiJ1MSJ9abcd" + "." + "c2lnbmF0dXJlMTIzNDU2"
	fakePEM    = "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIfakefakefake\nZmFrZQ==\n-----END RSA " + "PRIVATE KEY-----"
	fakeAPIKey = "api" + "_key"
)

func TestMaskSecrets(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
		rules          map[string]int
	}{
		{"private key", "before\n" + fakePEM + "\nafter", "before\n[REDACTED:private-key]\nafter", map[string]int{"private-key": 1}},
		{"private key cut off", "x -----BEGIN PRIVATE KEY-----\nMIIfake", "x [REDACTED:private-key]", map[string]int{"private-key": 1}},
		{"aws", "key " + fakeAWS + " here", "key [REDACTED:aws-key] here", map[string]int{"aws-key": 1}},
		{"google", "g=" + fakeGoogle, "g=[REDACTED:google-api-key]", map[string]int{"google-api-key": 1}},
		{"github", "use " + fakeGitHub, "use [REDACTED:github-token]", map[string]int{"github-token": 1}},
		{"slack", fakeSlack, "[REDACTED:slack-token]", map[string]int{"slack-token": 1}},
		{"stripe", fakeStripe, "[REDACTED:stripe-key]", map[string]int{"stripe-key": 1}},
		{"sk key", "OPENAI " + fakeSK, "OPENAI [REDACTED:openai-key]", map[string]int{"openai-key": 1}},
		{"pipeline token", "run with " + fakeKP + ".", "run with [REDACTED:pipeline-token].", map[string]int{"pipeline-token": 1}},
		{"jwt", "id_tok " + fakeJWT, "id_tok [REDACTED:jwt]", map[string]int{"jwt": 1}},
		{"bearer", "Authorization: Bearer " + strings.Repeat("abcd", 8), "Authorization: Bearer [REDACTED:bearer]", map[string]int{"bearer": 1}},
		{"url credentials", "postgres://admin:s3cretpw@db.example.com/app", "postgres://admin:[REDACTED:url-credentials]@db.example.com/app", map[string]int{"url-credentials": 1}},
		{"url param", "GET https://example.com/dl?token=abcdefghijklmnop1234&x=1", "GET https://example.com/dl?token=[REDACTED:url-secret-param]&x=1", map[string]int{"url-secret-param": 1}},
		{"password colon", "Password: Hunter22Hunter", "Password: [REDACTED:key-value]", map[string]int{"key-value": 1}},
		{"env style", "DB_PASSWORD=pw12345678", "DB_PASSWORD=[REDACTED:key-value]", map[string]int{"key-value": 1}},
		{"json", `{"` + fakeAPIKey + `": "abcd1234efgh", "n": 1}`, `{"` + fakeAPIKey + `": "[REDACTED:key-value]", "n": 1}`, map[string]int{"key-value": 1}},
		{"json in json", `{"log":"{\"secret\":\"abcd1234efgh\"}"}`, `{"log":"{\"secret\":\"[REDACTED:key-value]\"}"}`, map[string]int{"key-value": 1}},
		{"prose then value", "the password is Tr0ub4dor99", "the password is [REDACTED:key-value]", map[string]int{"key-value": 1}},
		{"two of one rule", "a=" + fakeAWS + " b=" + fakeAWS, "a=[REDACTED:aws-key] b=[REDACTED:aws-key]", map[string]int{"aws-key": 2}},
		{"colour codes", "\x1b[31mtoken=abcd1234efgh\x1b[0m done", "token=[REDACTED:key-value] done", map[string]int{"key-value": 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, rules := MaskSecrets(c.in)
			if got != c.want || !reflect.DeepEqual(rules, c.rules) {
				t.Fatalf("got %q %v\nwant %q %v", got, rules, c.want, c.rules)
			}
			// a second pass finds nothing more
			again, rules2 := MaskSecrets(got)
			if again != got || len(rules2) != 0 {
				t.Fatalf("not idempotent: %q %v", again, rules2)
			}
		})
	}
}

// Text that only looks like a secret stays as it is.
func TestMaskSecretsFalsePositives(t *testing.T) {
	for _, in := range []string{
		"Run started 2026-11-23T10:15:00Z, finished 2026-11-23",
		"token_expiry: 2026-12-01T10:00:00Z",
		"token" + "Fee: 1234abcd5678",
		"password_length: 123456789",
		"token: 12345678",
		"password: 1234",
		"password: changeme123",
		"password = your-password-1",
		"password=<your-password>",
		"password: ********",
		"password = $DB_PASSWORD_1",
		"secret: process.env.SECRET_1234",
		"password: null",
		"The token is regenerated daily.",
		"The auth token is valid for one hour.",
		"Your password is incorrect, try again",
		"The token limit is 4096 tokens per request.",
		"Use the bearer of the letter",
		"sk-short and kp-123 are too short",
		"AKIA is a prefix, and AKIAshort is not a key",
		"git clone ssh://git@example.com/acme/repo.git",
		"see http://localhost:3000/a@b and https://example.com:8080/path",
		"https://example.com/list?page=2&sort=name&key=short&token=abc",
		"commit 3f786850e387550fdab836ed7e6dc881de23001b merged",
		"call 555-0100 or order 1234567",
		"",
	} {
		got, rules := MaskSecrets(in)
		if got != in || len(rules) != 0 {
			t.Errorf("%q changed to %q %v", in, got, rules)
		}
	}
}

func TestStripControl(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[31mred\x1b[0m plain":                            "red plain",
		"\x1b[2K\x1b[1Gprogress":                              "progress",
		"\x1b]0;window title\x07text":                         "text",
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\": "link",
		"a\x00b\x07c\rd\x7fe\u0085f":                          "abcdef",
		"keep\ttab\nand newline":                              "keep\ttab\nand newline",
		"héllo ✓ \U0001F600":                                  "héllo ✓ \U0001F600",
		"":                                                    "",
	} {
		if got := StripControl(in); got != want {
			t.Errorf("StripControl(%q) = %q, want %q", in, got, want)
		}
	}
}

// Counts name the rules and never carry a value.
func TestMaskSecretsKeepsValuesOut(t *testing.T) {
	in := strings.Join([]string{fakePEM, fakeAWS, fakeKP, fakeSK, fakeJWT, "password: Hunter22Hunter"}, "\n")
	out, rules := MaskSecrets(in)
	for _, v := range []string{"MIIfake", fakeAWS, fakeKP, fakeSK, fakeJWT, "Hunter22Hunter"} {
		if strings.Contains(out, v) {
			t.Errorf("output still holds %q", v)
		}
	}
	if len(rules) != 6 {
		t.Errorf("rules = %v", rules)
	}
}
