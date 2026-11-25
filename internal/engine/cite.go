package engine

import "sync"

// cites hands out the citation numbers of one run: the passages in the system
// prompt and the results of every tool call share one numbering, so [n] means
// the same passage wherever the model read it, and the sources list the web
// app shows is keyed by the same numbers. A key (see retrieval.Hit.CiteKey)
// that comes up again, in the prompt or in a later search, keeps its number.
type cites struct {
	mu   sync.Mutex
	nums map[string]int
}

// number returns the citation number of key, giving it the next one if new.
func (c *cites) number(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.nums[key]; ok {
		return n
	}
	if c.nums == nil {
		c.nums = map[string]int{}
	}
	c.nums[key] = len(c.nums) + 1
	return len(c.nums)
}
