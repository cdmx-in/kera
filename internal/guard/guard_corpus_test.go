package guard

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The corpus (testdata/corpus.tsv) holds labelled lines: "pos" = the text contains one real instance of
// the rule (or an attack, for "injection"), "neg" = it must not trigger it. TestCorpus measures every
// detector on it and pins the result, so a change to a regex or checksum that moves precision or recall
// shows up as a failing test with the numbers, not as a surprise in production.
// Some negatives are undecidable by format (a 16-digit id that passes Luhn, a part code shaped like a PAN):
// they stay in the corpus on purpose and appear as pinned false positives.

type corpusRow struct{ label, rule, text string }

func readCorpus(t *testing.T) []corpusRow {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "corpus.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []corpusRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.SplitN(line, "\t", 3)
		if len(p) != 3 || (p[0] != "pos" && p[0] != "neg") {
			t.Fatalf("bad corpus line: %q", line)
		}
		rows = append(rows, corpusRow{p[0], p[1], p[2]})
	}
	return rows
}

func detect(text string) map[string]int {
	_, hits := Scan(text)
	if n := ScanInjection(text); n > 0 {
		hits["injection"] = n
	}
	return hits
}

// pinned: rule -> {TP, FP, FN, TN}, measured on corpus.tsv. Update deliberately, with the reason in the commit.
var pinned = map[string][4]int{
	"injection":   {15, 5, 10, 20},
	"pii.aadhaar": {8, 1, 0, 7},
	"pii.card":    {8, 2, 0, 7},
	"pii.email":   {8, 2, 0, 8},
	"pii.iban":    {15, 0, 0, 15},
	"pii.pan":     {8, 2, 0, 6},
	"pii.phone":   {8, 3, 0, 9},
	"pii.ssn":     {8, 1, 0, 7},
	"pii.nino":    {8, 0, 0, 8},
}

func TestCorpus(t *testing.T) {
	rows := readCorpus(t)
	got := map[string]*[4]int{}
	collisions := map[string]int{}
	for _, r := range rows {
		hits := detect(r.text)
		c := got[r.rule]
		if c == nil {
			c = &[4]int{}
			got[r.rule] = c
		}
		fired := hits[r.rule] > 0
		switch {
		case r.label == "pos" && fired:
			c[0]++
		case r.label == "neg" && fired:
			c[1]++
		case r.label == "pos":
			c[2]++
		default:
			c[3]++
		}
		for rule := range hits { // another rule fired on this line: only logged, the order of rules is part of the design
			if rule != r.rule {
				collisions[rule+" on "+r.rule+" lines"]++
			}
		}
	}
	rules := make([]string, 0, len(got))
	for r := range got {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-12s %4s %4s %4s %4s  %9s %6s %6s\n", "rule", "TP", "FP", "FN", "TN", "precision", "recall", "FPR")
	for _, r := range rules {
		c := got[r]
		fmt.Fprintf(&b, "%-12s %4d %4d %4d %4d  %9s %6s %6s\n", r, c[0], c[1], c[2], c[3], ratio(c[0], c[0]+c[1]), ratio(c[0], c[0]+c[2]), ratio(c[1], c[1]+c[3]))
	}
	for k, n := range collisions {
		fmt.Fprintf(&b, "cross-rule: %s: %d\n", k, n)
	}
	t.Log(b.String())
	if len(pinned) == 0 {
		t.Skip("no pinned values yet")
	}
	for _, r := range rules {
		if want, ok := pinned[r]; !ok || want != *got[r] {
			t.Errorf("%s: measured {TP FP FN TN} = %v, pinned %v (update pinned if the change is intended)", r, *got[r], pinned[r])
		}
	}
}

func ratio(a, b int) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", float64(a)/float64(b))
}

// TestScanDir counts hits per rule over real markdown (GUARD_SCAN_DIR=/path). It prints rule, file:line and
// never the matched text, so a scan of a knowledge base does not copy personal data into logs.
func TestScanDir(t *testing.T) {
	dir := os.Getenv("GUARD_SCAN_DIR")
	if dir == "" {
		t.Skip("set GUARD_SCAN_DIR to scan a folder of .md files")
	}
	total, lines := map[string]int{}, 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for n := 1; sc.Scan(); n++ {
			lines++
			for rule, c := range detect(sc.Text()) {
				total[rule] += c
				if total[rule] <= 25 { // the first hits per rule are enough to judge by hand
					t.Logf("%s %s:%d", rule, p, n)
				}
			}
		}
		return nil
	})
	t.Logf("scanned %d lines; hits per rule: %v", lines, total)
}
