package ids

import (
	"regexp"
	"testing"
)

var uuidV7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNew(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for i := 0; i < 1000; i++ {
		id := New()
		if !uuidV7.MatchString(id) {
			t.Fatalf("not a v7 uuid: %s", id)
		}
		if seen[id] {
			t.Fatalf("duplicate: %s", id)
		}
		seen[id] = true
		if id[:13] < prev[:min(13, len(prev))] {
			t.Fatalf("timestamp went backwards: %s after %s", id, prev)
		}
		prev = id
	}
}
