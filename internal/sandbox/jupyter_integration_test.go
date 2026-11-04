package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/tools"
)

// TestRealJupyter runs execute_code against a real Jupyter server:
//
//	KERA_TEST_JUPYTER_URL=http://<ip>:8888 KERA_TEST_JUPYTER_TOKEN=... \
//	KERA_TEST_JUPYTER_SHARED=<host folder mounted in the kernel> [KERA_TEST_JUPYTER_KERNEL_DIR=/mnt/data]
func TestRealJupyter(t *testing.T) {
	e := realJupyter(t)
	ctx := context.Background()
	in := e.upload(t, "alice", "sales.csv", "region,amount\nnorth,10\nsouth,32\n")
	code := `import pandas as pd
import matplotlib
matplotlib.use("module://matplotlib_inline.backend_inline")
import matplotlib.pyplot as plt
df = pd.read_csv(ATTACHMENTS[0])
df.to_csv("summary.csv", index=False)
df.to_excel("/mnt/data/summary.xlsx", index=False)
df.plot.bar(x="region", y="amount"); plt.show()
print("total", df.amount.sum())
df.amount.sum() * 2`
	res := e.call(t, code, in)
	t.Logf("result: %s", res.Content)
	if res.IsError {
		t.Fatalf("error: %s", res.Content)
	}
	var rep report
	_ = json.Unmarshal([]byte(res.Content), &rep)
	if !strings.Contains(rep.Stdout, "total 42") || rep.Result != "np.int64(84)" && rep.Result != "84" {
		t.Fatalf("output = %+v", rep)
	}
	got := map[string]int64{}
	for _, id := range res.Files {
		f, err := e.p.Files.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got[f.Filename] = f.Size
	}
	for _, want := range []string{"summary.csv", "summary.xlsx", "output-1.png"} {
		if got[want] == 0 {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if got["sales.csv"] != 0 {
		t.Error("unchanged input returned")
	}
	res = e.call(t, "1/0")
	if !res.IsError || !strings.Contains(res.Content, "ZeroDivisionError") {
		t.Fatalf("error case = %s", res.Content)
	}
}

// realJupyter is setup pointed at the Jupyter server from the environment.
func realJupyter(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("KERA_TEST_JUPYTER_URL")
	if url == "" {
		t.Skip("KERA_TEST_JUPYTER_URL not set")
	}
	e := setup(t)
	ctx := context.Background()
	kernelDir := os.Getenv("KERA_TEST_JUPYTER_KERNEL_DIR")
	if kernelDir == "" {
		kernelDir = "/mnt/data"
	}
	enc, _ := e.p.Keys.Encrypt([]byte(os.Getenv("KERA_TEST_JUPYTER_TOKEN")))
	for k, v := range map[string]any{
		"code_interpreter.url": url, "code_interpreter.timeout": 60, "code_interpreter.prelude": "",
		"code_interpreter.shared_dir": os.Getenv("KERA_TEST_JUPYTER_SHARED"), "code_interpreter.kernel_dir": kernelDir,
		"code_interpreter.token": map[string]string{"enc": base64.StdEncoding.EncodeToString(enc)},
	} {
		if err := e.p.Settings.Set(ctx, k, v, true); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// TestRealJupyterPolyglot needs the polyglot sandbox image (kera-jupyter with
// php, node, ruby, go, java, R); same environment as TestRealJupyter.
func TestRealJupyterPolyglot(t *testing.T) {
	e := realJupyter(t)
	ctx := context.Background()
	specs, err := e.p.Tools(ctx, e.user)
	if err != nil || len(specs) != 1 {
		t.Fatalf("tools: %v %v", specs, err)
	}
	t.Logf("schema: %s", specs[0].Parameters)
	t.Logf("prompt: %s", Prompt(ctx, e.p.Settings))
	hello := map[string]string{
		"php":        `<?php echo "hello from php " . PHP_VERSION . "\n";`,
		"node":       `console.log("hello from node " + process.version)`,
		"typescript": "const who: string = 'typescript'\nconsole.log(`hello from ${who} ${process.version}`)",
		"bash":       `echo "hello from bash $BASH_VERSION"`,
		"ruby":       `puts "hello from ruby #{RUBY_VERSION}"`,
		"go":         "package main\n\nimport (\n\t\"fmt\"\n\t\"runtime\"\n)\n\nfunc main() { fmt.Println(\"hello from go\", runtime.Version()) }\n",
		"r":          `cat("hello from R", R.version.string, "\n")`,
		"java":       `public class Hello { public static void main(String[] a) { System.out.println("hello from java " + Runtime.version()); } }`,
	}
	for lang, code := range hello {
		t.Run(lang, func(t *testing.T) {
			rep, res := e.callLang(t, lang, code)
			if res.IsError || rep.ExitCode == nil || *rep.ExitCode != 0 || !strings.Contains(rep.Stdout, "hello from ") || rep.Stderr != "" {
				t.Fatalf("%s: %s", lang, res.Content)
			}
			t.Logf("%s: %s", lang, strings.TrimSpace(rep.Stdout))
		})
	}
	in := e.upload(t, "alice", "sales.csv", "region,amount\nnorth,10\nsouth,32\n")
	php := `<?php
$rows = array_map('str_getcsv', file('sales.csv', FILE_IGNORE_NEW_LINES));
$out = fopen('totals.csv', 'w');
fputcsv($out, ['region', 'double']);
foreach (array_slice($rows, 1) as [$region, $amount]) { fputcsv($out, [$region, $amount * 2]); }
fclose($out);
echo "rows: ", count($rows) - 1, "\n";
fwrite(STDERR, "done\n");`
	rep, res := e.callLang(t, "php", php, in)
	if res.IsError || !strings.Contains(rep.Stdout, "rows: 2") || !strings.Contains(rep.Stderr, "done") || len(res.Files) != 1 {
		t.Fatalf("php csv: %s", res.Content)
	}
	f, body := mustOpen(t, e, res.Files[0])
	if f.Filename != "totals.csv" || body != "region,double\nnorth,20\nsouth,64" {
		t.Fatalf("harvested %s %q", f.Filename, body)
	}
	rep, res = e.callLang(t, "bash", "echo partial; exit 7")
	if !res.IsError || rep.ExitCode == nil || *rep.ExitCode != 7 || rep.Stdout != "partial\n" {
		t.Fatalf("exit code: %s", res.Content)
	}
}

func (e *env) callLang(t *testing.T, lang, code string, fileIDs ...string) (report, tools.Result) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"code": code, "language": lang})
	res, err := e.p.Execute(context.Background(), tools.Call{Name: "execute_code", Args: args, User: e.user, RunID: "run1",
		ChatID: "c1", FileIDs: fileIDs, Emit: func(tools.Event) {}})
	if err != nil {
		t.Fatal(err)
	}
	var rep report
	_ = json.Unmarshal([]byte(res.Content), &rep)
	return rep, res
}
