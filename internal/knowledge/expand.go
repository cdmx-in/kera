package knowledge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/retrieval"
)

// expand widens each hit to the chunks around it, so the model reads the
// passage and not a lone 1,000-character cut of it. For every hit it takes
// the n chunks before and after (same file, collection and embedding model),
// joins runs of neighbouring chunks into one passage, cuts a run that would
// pass maxChars into several, and drops pieces that hold no hit. Each chunk
// appears in at most one passage. Files come in the order of their best hit
// and the passages of a file in document order. n <= 0 returns hits as they are.
func (s *Service) expand(ctx context.Context, hits []retrieval.Hit, n, maxChars int) ([]retrieval.Hit, error) {
	if n <= 0 || len(hits) == 0 {
		return hits, nil
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.ChunkID
	}
	// ponytail: the join walks every chunk of the hit's file and filters on idx;
	// an index on (knowledge_id, file_id, idx) is the upgrade for files of thousands of chunks.
	rows, err := s.db.Query(ctx, `SELECT DISTINCT b.knowledge_id, b.file_id, c.idx, c.text
		FROM chunk b JOIN chunk c ON c.knowledge_id = b.knowledge_id AND c.file_id = b.file_id
			AND c.embedding_model_id = b.embedding_model_id AND c.idx BETWEEN b.idx - $2 AND b.idx + $2
		WHERE b.id = ANY($1)`, ids, n)
	if err != nil {
		return nil, fmt.Errorf("load neighbouring chunks: %w", err)
	}
	type fileKey struct{ knowledge, file string }
	texts := map[fileKey]map[int]string{}
	for rows.Next() {
		var k fileKey
		var idx int
		var text string
		if err := rows.Scan(&k.knowledge, &k.file, &idx, &text); err != nil {
			rows.Close()
			return nil, err
		}
		if texts[k] == nil {
			texts[k] = map[int]string{}
		}
		texts[k][idx] = text
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var order []fileKey
	byFile := map[fileKey][]retrieval.Hit{}
	for _, h := range hits {
		k := fileKey{h.KnowledgeID, h.FileID}
		if _, ok := byFile[k]; !ok {
			order = append(order, k)
		}
		byFile[k] = append(byFile[k], h)
	}
	var out []retrieval.Hit
	for _, k := range order {
		out = append(out, passages(byFile[k], texts[k], n, maxChars)...)
	}
	return out, nil
}

// passages builds the passages of one file from its hits and the text of the
// chunks around them (by chunk index).
func passages(hits []retrieval.Hit, text map[int]string, n, maxChars int) []retrieval.Hit {
	best := map[int]retrieval.Hit{} // hit chunk index -> hit
	for _, h := range hits {
		if _, ok := text[h.Chunk]; !ok {
			text[h.Chunk] = h.Text
		}
		if b, ok := best[h.Chunk]; !ok || h.Score > b.Score {
			best[h.Chunk] = h
		}
	}
	var idxs []int
	for _, h := range hits {
		for i := h.Chunk - n; i <= h.Chunk+n; i++ {
			if _, ok := text[i]; ok && !slices.Contains(idxs, i) {
				idxs = append(idxs, i)
			}
		}
	}
	slices.Sort(idxs)
	var out []retrieval.Hit
	var cur *retrieval.Hit
	hit := false // cur holds a hit, not only neighbours
	flush := func() {
		if cur != nil && hit {
			out = append(out, *cur)
		}
		cur, hit = nil, false
	}
	for _, i := range idxs {
		if cur != nil && (i != cur.ChunkEnd+1 || runes(cur.Text)+runes(text[i]) > maxChars) {
			flush()
		}
		if cur == nil {
			cur = &retrieval.Hit{KnowledgeID: hits[0].KnowledgeID, FileID: hits[0].FileID, FileName: hits[0].FileName,
				Text: text[i], Chunk: i, ChunkEnd: i}
		} else {
			cur.Text, cur.ChunkEnd = joinChunks(cur.Text, text[i]), i
		}
		if h, ok := best[i]; ok && (!hit || h.Score > cur.Score) {
			// the piece describes itself by its best hit
			cur.Score, cur.Page, cur.Headers, cur.ChunkID, hit = h.Score, h.Page, h.Headers, h.ChunkID, true
		}
	}
	flush()
	return out
}

// joinChunks appends next to prev. Neighbouring chunks share the end of one
// and the start of the next (rag.chunk_overlap), so the longest such overlap
// is written once; chunks that do not overlap are joined by a blank line.
// ponytail: an overlap shorter than 10 bytes is not looked for, and a repeated
// 10-byte tail would be trimmed too; both only change whitespace-level text.
func joinChunks(prev, next string) string {
	for l := min(len(prev), len(next), 500); l >= 10; l-- {
		if l < len(next) && !utf8.RuneStart(next[l]) {
			continue
		}
		// the chunk before was trimmed, the overlap at the start of this one was not
		if o := strings.TrimRight(next[:l], " \t\r\n"); len(o) >= 10 && strings.HasSuffix(prev, o) {
			return prev + next[len(o):]
		}
	}
	return prev + "\n\n" + next
}
