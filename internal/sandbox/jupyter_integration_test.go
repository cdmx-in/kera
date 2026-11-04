package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRealJupyter runs execute_code against a real Jupyter server:
//
//	KERA_TEST_JUPYTER_URL=http://<ip>:8888 KERA_TEST_JUPYTER_TOKEN=... \
//	KERA_TEST_JUPYTER_SHARED=<host folder mounted in the kernel> [KERA_TEST_JUPYTER_KERNEL_DIR=/mnt/data]
func TestRealJupyter(t *testing.T) {
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
