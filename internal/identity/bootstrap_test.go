package identity

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
)

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.a.DB.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestBootstrapAdmin(t *testing.T) {
	l := captureLogs(t)
	e := community(t)
	ctx := context.Background()
	e.a.Env.AdminEmail, e.a.Env.AdminPassword = " Root@Example.com", pwRoot

	// An empty install gets its admin, who can sign in with the password.
	if err := bootstrapAdmin(ctx, e.a); err != nil {
		t.Fatal(err)
	}
	var role, name string
	if err := e.a.DB.QueryRow(ctx, `SELECT role, name FROM "user" WHERE email = 'root@example.com'`).Scan(&role, &name); err != nil || role != "admin" || name != "root" {
		t.Fatalf("admin row: role=%q name=%q err=%v", role, name, err)
	}
	if r := e.signinAs("root@example.com", pwRoot, ""); r.Code != 200 {
		t.Fatalf("the bootstrap admin cannot sign in: %d %s", r.Code, r.Body)
	}
	if strings.Count(l.String(), "first admin created") != 1 || strings.Contains(l.String(), pwRoot) || strings.Contains(l.String(), "argon2") {
		t.Fatalf("log should say once that the admin was created, and never show the password:\n%s", l.String())
	}

	// Every later start leaves everything alone, even with another password or email.
	var before string
	_ = e.a.DB.QueryRow(ctx, `SELECT hash FROM auth_password`).Scan(&before)
	for _, env := range [][2]string{{"root@example.com", pwRoot}, {"root@example.com", "a-different-password"}, {"other@example.com", pwRoot}} {
		e.a.Env.AdminEmail, e.a.Env.AdminPassword = env[0], env[1]
		if err := bootstrapAdmin(ctx, e.a); err != nil {
			t.Fatal(err)
		}
	}
	var after string
	_ = e.a.DB.QueryRow(ctx, `SELECT hash FROM auth_password`).Scan(&after)
	if n := e.count(`SELECT count(*) FROM "user"`); n != 1 || before != after {
		t.Fatalf("users=%d, hash changed=%v", n, before != after)
	}
	if r := e.signinAs("root@example.com", pwRoot, ""); r.Code != 200 {
		t.Fatalf("original password stopped working: %d", r.Code)
	}
}

// An install that already has users is never touched, whatever the variables say.
func TestBootstrapAdminSkipsAnExistingInstall(t *testing.T) {
	e := community(t)
	ctx := context.Background()
	e.mkUser("user", "ann@example.com")
	e.a.Env.AdminEmail, e.a.Env.AdminPassword = "root@example.com", pwRoot
	if err := bootstrapAdmin(ctx, e.a); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM "user"`) != 1 || e.count(`SELECT count(*) FROM auth_password`) != 0 {
		t.Fatal("bootstrap changed an install that has users")
	}
	// It does not even complain about a bad password there: nothing is read.
	e.a.Env.AdminPassword = "x"
	if err := bootstrapAdmin(ctx, e.a); err != nil {
		t.Fatalf("a bad password on an existing install is an error: %v", err)
	}
}

func TestBootstrapAdminNeedsBothAndGoodValues(t *testing.T) {
	e := community(t)
	ctx := context.Background()
	for _, c := range []struct{ email, pw, want string }{
		{"", "", ""},
		{"root@example.com", "", ""}, // half a setting: nothing happens
		{"", pwRoot, ""},
		{"not an email", pwRoot, "KERA_ADMIN_EMAIL"},
		{"root@example.com", "short", "KERA_ADMIN_PASSWORD"},
		{"root@example.com", "root@example.com", "KERA_ADMIN_PASSWORD"},
	} {
		e.a.Env.AdminEmail, e.a.Env.AdminPassword = c.email, c.pw
		err := bootstrapAdmin(ctx, e.a)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%q / %q: err = %v, want one naming %q", c.email, c.pw, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), c.pw) && c.pw != "" {
			t.Errorf("the error repeats the password: %v", err)
		}
		if e.count(`SELECT count(*) FROM "user"`) != 0 {
			t.Fatalf("%q / %q created a user", c.email, c.pw)
		}
	}
}

// Two servers starting together make one admin.
func TestBootstrapAdminOnce(t *testing.T) {
	e := community(t)
	e.a.Env.AdminEmail, e.a.Env.AdminPassword = "root@example.com", pwRoot
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := bootstrapAdmin(context.Background(), e.a); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if e.count(`SELECT count(*) FROM "user"`) != 1 || e.count(`SELECT count(*) FROM auth_password`) != 1 {
		t.Fatal("parallel starts made more than one admin")
	}
}

func TestUserPasswdAndAddCLI(t *testing.T) {
	l := captureLogs(t)
	e := community(t)
	ctx := context.Background()
	var out bytes.Buffer
	run := func(stdin string, args ...string) error {
		out.Reset()
		return UserCmd(ctx, e.a.DB, args, strings.NewReader(stdin), &out)
	}

	// add --password-stdin: the user can sign in at once; the password never reaches the output.
	if err := run(pwAnn+"\n", "add", "--password-stdin", "--name", "Ann Lee", "Ann@Example.com"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), pwAnn) || !strings.Contains(out.String(), "ann@example.com") {
		t.Fatalf("output %q", out.String())
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != 200 {
		t.Fatalf("sign-in after add: %d %s", r.Code, r.Body)
	}
	// without it: a user with no password, and a hint
	if err := run("", "add", "bo@example.com"); err != nil || !strings.Contains(out.String(), "kera user passwd bo@example.com") {
		t.Fatalf("add without a password: %v %q", err, out.String())
	}
	for name, c := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"duplicate":      {pwBob + "\n", []string{"add", "--password-stdin", "ann@example.com"}, "already exists"},
		"short password": {"short\n", []string{"add", "--password-stdin", "cy@example.com"}, "at least 10"},
		"email as pw":    {"cy@example.com\n", []string{"add", "--password-stdin", "cy@example.com"}, "not your email"},
		"empty stdin":    {"", []string{"add", "--password-stdin", "cy@example.com"}, "no password on standard input"},
		"bad email":      {pwBob + "\n", []string{"add", "--password-stdin", "cy@"}, "email address"},
		"no flag for it": {"", []string{"add", "--password", pwBob, "cy@example.com"}, "flag provided but not defined"},
	} {
		if err := run(c.stdin, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if e.count(`SELECT count(*) FROM "user" WHERE email = 'cy@example.com'`) != 0 {
		t.Fatal("a refused add left a user behind")
	}

	// passwd --stdin: sets it, signs the user out, accepts CRLF, handles a missing user.
	_, tok := e.mkUser("user", "dee@example.com")
	if err := run(pwNew+"\r\n", "passwd", "--stdin", "DEE@example.com"); err != nil {
		t.Fatal(err)
	}
	e.status(401, "GET", "/api/v1/auths/", tok, nil) // the epoch moved
	if r := e.signinAs("dee@example.com", pwNew, ""); r.Code != 200 {
		t.Fatalf("sign-in after passwd: %d %s", r.Code, r.Body)
	}
	if err := run(pwNew+"\n", "passwd", "--stdin", "nobody@example.com"); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("unknown user: %v", err)
	}
	if err := run("short\n", "passwd", "--stdin", "dee@example.com"); err == nil {
		t.Fatal("a short password was accepted")
	}
	if err := run("", "passwd", "--stdin", "dee@example.com"); err == nil {
		t.Fatal("an empty stdin was accepted")
	}
	if err := run(pwNew+"\n", "passwd", pwNew, "dee@example.com"); err == nil {
		t.Fatal("a password as an argument was accepted")
	}
	// without --stdin it needs a terminal: a pipe is refused, and says what to do
	pr, pw, _ := os.Pipe()
	defer pr.Close()
	defer pw.Close()
	out.Reset()
	if err := UserCmd(ctx, e.a.DB, []string{"passwd", "dee@example.com"}, pr, &out); err == nil || !strings.Contains(err.Error(), "--stdin") {
		t.Fatalf("passwd from a pipe without --stdin: %v", err)
	}
	if err := run("", "passwd", "dee@example.com"); err == nil || !strings.Contains(err.Error(), "--stdin") {
		t.Fatalf("passwd from a reader that is no terminal: %v", err)
	}
	if strings.Contains(l.String()+out.String(), pwNew) {
		t.Fatal("a password leaked into a log or the output")
	}
}

func TestPasswdCLITellsAboutAServerSideLock(t *testing.T) {
	e := community(t)
	e.mkUser("user", "dee@example.com")
	var out bytes.Buffer
	if err := UserCmd(context.Background(), e.a.DB, []string{"passwd", "--stdin", "dee@example.com"}, strings.NewReader(pwNew+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "15 minutes") || !strings.Contains(out.String(), "restarts") || strings.Contains(out.String(), pwNew) {
		t.Fatalf("output %q", out.String())
	}
}
