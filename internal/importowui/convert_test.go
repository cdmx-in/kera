package importowui

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSplitDetails(t *testing.T) {
	content := "Intro line.\n" +
		`<details type="reasoning" done="true" duration="3">` + "\n<summary>Thought for 3 seconds</summary>\n> first thought\n> second & last\n</details>\n" +
		"Let me look that up.\n" +
		`<details type="tool_calls" done="true" id="call_1" name="lookup" arguments="&quot;{\&quot;q\&quot;: \&quot;x\&quot;}&quot;" result="&quot;42&quot;" files="[{&quot;id&quot;: &quot;f1&quot;}]" embeds="">` + "\n<summary>Tool Executed</summary>\n</details>\n" +
		`<details type="tool_calls" done="false" id="call_2" name="pending" arguments="{}">` + "\n<summary>Executing...</summary>\n</details>\n" +
		"The answer is 42."
	text, parts := SplitDetails(content)
	if text != "Intro line.\n\nLet me look that up.\n\nThe answer is 42." {
		t.Fatalf("text = %q", text)
	}
	want := []Part{
		{"type": "text", "text": "Intro line."},
		{"type": "reasoning", "text": "first thought\nsecond & last", "done": true, "durationMs": int64(3000)},
		{"type": "text", "text": "Let me look that up."},
		{"type": "tool_call", "callId": "call_1", "name": "lookup", "arguments": `{"q": "x"}`},
		{"type": "tool_result", "callId": "call_1", "name": "lookup", "result": "42", "files": []any{map[string]any{"id": "f1"}}},
		{"type": "tool_call", "callId": "call_2", "name": "pending", "arguments": "{}"},
		{"type": "text", "text": "The answer is 42."},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts:\n got %#v\nwant %#v", parts, want)
	}
}

func TestSplitDetailsLeavesOtherBlocks(t *testing.T) {
	for _, c := range []string{
		"plain answer",
		`<details type="code_interpreter" done="true"><summary>Analyzed</summary>code</details> after`,
		`<details type="reasoning" done="true"><summary>x</summary>never closed`,
	} {
		text, parts := SplitDetails(c)
		if text != c || parts != nil {
			t.Fatalf("SplitDetails(%q) = %q, %v", c, text, parts)
		}
	}
}

func TestSplitDetailsNested(t *testing.T) {
	c := `<details type="reasoning" done="true"><summary>s</summary>> a <details><summary>in</summary>b</details> c</details>tail`
	text, parts := SplitDetails(c)
	if text != "tail" || len(parts) != 2 || parts[0]["type"] != "reasoning" {
		t.Fatalf("got %q %v", text, parts)
	}
}

func TestOutputParts(t *testing.T) {
	var items []map[string]any
	if err := json.Unmarshal([]byte(`[
		{"type":"reasoning","id":"r1","content":[{"type":"text","text":"thinking"}],"duration":1.5,"status":"completed"},
		{"type":"function_call","call_id":"c1","name":"search","arguments":"{\"q\":1}","status":"completed","duration":2},
		{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"hits"}],"status":"completed"},
		{"type":"web_search_call","id":"w1","status":"completed","action":{"type":"search","query":"go"}},
		{"type":"vendor:context_compaction","id":"x","summary":"short"},
		{"type":"something_new"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
	]`), &items); err != nil {
		t.Fatal(err)
	}
	got := OutputParts(items)
	want := []Part{
		{"type": "reasoning", "text": "thinking", "done": true, "durationMs": int64(1500)},
		{"type": "tool_call", "callId": "c1", "name": "search", "arguments": `{"q":1}`, "status": "completed", "durationMs": int64(2000)},
		{"type": "tool_result", "callId": "c1", "name": "search", "result": "hits", "status": "completed"},
		{"type": "status", "action": "web_search", "done": true, "query": "go"},
		{"type": "compaction", "summary": "short"},
		{"type": "text", "text": "done"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// A synthetic tree:  u1 -> a1 (stopped) ; u1 -> a2 (regenerated) -> u2 -> a3
// plus a dangling child of a deleted message and a role-less leftover entry.
const historyFixture = `{"history":{"currentId":"a2","messages":{
 "u1":{"id":"u1","parentId":null,"childrenIds":["a1","a2"],"role":"user","content":"hello","timestamp":1700000000,
       "files":[{"type":"file","id":"f1","name":"a.txt","file":{"id":"f1","data":{"content":"big"}}}]},
 "a1":{"id":"a1","parentId":"u1","childrenIds":[],"role":"assistant","model":"m1","content":"partial","done":false,"timestamp":1700000001},
 "a2":{"id":"a2","parentId":"u1","childrenIds":["u2"],"role":"assistant","model":"m1","timestamp":1700000002,
       "content":"<details type=\"reasoning\" done=\"true\" duration=\"1\">\n<summary>Thought</summary>\n> hmm\n</details>\nHi!",
       "statusHistory":[{"action":"web_search","description":"Searching","done":true}],
       "usage":{"prompt_tokens":5},"sources":[{"source":{"id":"k"}}],"followUps":["more?"]},
 "u2":{"id":"u2","parentId":"a2","childrenIds":["a3"],"role":"user","content":[{"type":"text","text":"and?"}],"timestamp":1700000003},
 "a3":{"id":"a3","parentId":"u2","childrenIds":[],"role":"assistant","content":"stale","timestamp":1700000004,"error":{"content":"boom"}},
 "o1":{"id":"o1","parentId":"gone","childrenIds":[],"role":"assistant","content":"orphan","timestamp":1700000005},
 "zz":{"followUps":[]}
}}}`

func TestConvertHistory(t *testing.T) {
	outputs := map[string][]map[string]any{
		"a3": {{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "from output"}}}},
	}
	tr, err := ConvertHistory([]byte(historyFixture), outputs, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if tr.NoRole != 1 || tr.Dangling != 1 || len(tr.Messages) != 6 {
		t.Fatalf("noRole=%d dangling=%d n=%d", tr.NoRole, tr.Dangling, len(tr.Messages))
	}
	by := map[string]Message{}
	ids := []string{}
	for _, m := range tr.Messages {
		by[m.ID] = m
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, []string{"u1", "a1", "a2", "u2", "a3", "o1"}) {
		t.Fatalf("order %v", ids)
	}
	// currentId a2 is not a leaf: it is walked down to a3.
	if tr.CurrentID != "a3" {
		t.Fatalf("current %q", tr.CurrentID)
	}
	if by["o1"].ParentID != "" || by["a2"].ParentID != "u1" {
		t.Fatal("parent links wrong")
	}
	if by["a1"].Status != "stopped" || by["a3"].Status != "error" || by["a3"].Error != "boom" || by["a2"].Status != "done" {
		t.Fatalf("statuses %s %s %s", by["a1"].Status, by["a3"].Status, by["a2"].Status)
	}
	if f := by["u1"].Files[0].(map[string]any); f["file"] != nil || f["id"] != "f1" {
		t.Fatalf("file ref not stripped: %v", f)
	}
	if by["u2"].Content != "and?" {
		t.Fatalf("multimodal content %q", by["u2"].Content)
	}
	a2 := by["a2"]
	if a2.Content != "Hi!" || len(a2.Parts) != 3 || a2.Parts[0]["type"] != "status" || a2.Parts[1]["type"] != "reasoning" || a2.Parts[2]["text"] != "Hi!" {
		t.Fatalf("a2 = %q %v", a2.Content, a2.Parts)
	}
	if a2.Usage["prompt_tokens"] != float64(5) || len(a2.Sources) != 1 || len(a2.FollowUps) != 1 || a2.Timestamp != 1700000002 {
		t.Fatalf("a2 extras %v %v %v %d", a2.Usage, a2.Sources, a2.FollowUps, a2.Timestamp)
	}
	if p := by["a3"].Parts; len(p) != 1 || p[0]["text"] != "from output" || by["a3"].Content != "stale" {
		t.Fatalf("a3 parts %v", p)
	}
	if len(by["a1"].Parts) != 0 {
		t.Fatalf("plain message got parts %v", by["a1"].Parts)
	}
}

func TestActiveLeafUnknownCurrent(t *testing.T) {
	msgs := []Message{{ID: "r"}, {ID: "x", ParentID: "r"}, {ID: "y", ParentID: "r"}}
	if got := activeLeaf(msgs, "missing"); got != "y" {
		t.Fatalf("got %q", got)
	}
	if got := activeLeaf(nil, "a"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestEpoch(t *testing.T) {
	want := time.Unix(1700000000, 0).UTC()
	for _, v := range []int64{1700000000, 1700000000000, 1700000000000000, 1700000000000000000} {
		if got := epoch(v); !got.Equal(want) {
			t.Fatalf("epoch(%d) = %v", v, got)
		}
	}
}
