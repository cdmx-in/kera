package files

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// Preview caps.
const (
	MaxPreviewRows  = 2000
	MaxPreviewCols  = 200
	MaxPreviewBytes = 200 << 10
)

// Preview is a table-shaped view of a file for the preview modal.
type Preview struct {
	Kind      string     `json:"kind"` // csv | xlsx | text | pdf | image | docx | other
	Sheets    []string   `json:"sheets"`
	Sheet     int        `json:"sheet"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

// PreviewKind classifies a file for previewing.
func PreviewKind(f File) string {
	e := ext(f)
	ct := f.ContentType
	switch {
	case e == ".csv" || e == ".tsv" || strings.HasPrefix(ct, "text/csv") || strings.HasPrefix(ct, "text/tab-separated-values"):
		return "csv"
	case e == ".xlsx" || e == ".xlsm":
		return "xlsx"
	case e == ".pdf" || ct == "application/pdf":
		return "pdf"
	case strings.HasPrefix(ct, "image/"):
		return "image"
	case e == ".docx":
		return "docx"
	case textExts[e] || e == ".html" || e == ".htm" || strings.HasPrefix(ct, "text/"):
		return "text"
	}
	return "other"
}

// Preview builds the preview of file id; sheet selects an xlsx sheet (0-based).
func (s *Service) Preview(ctx context.Context, id string, sheet int) (Preview, error) {
	rc, f, err := s.Open(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	defer rc.Close()
	p := Preview{Kind: PreviewKind(f), Sheets: []string{}, Rows: [][]string{}}
	switch p.Kind {
	case "csv":
		comma := ','
		if ext(f) == ".tsv" || strings.HasPrefix(f.ContentType, "text/tab-separated-values") {
			comma = '\t'
		}
		p.Rows, p.Truncated, err = csvRows(rc, comma)
	case "xlsx":
		p.Sheet = sheet
		p.Sheets, p.Rows, p.Truncated, err = xlsxRows(rc, sheet)
	case "text":
		p.Rows, p.Truncated, err = textRows(rc)
	case "docx":
		var t string
		if t, err = s.Text(ctx, id); err == nil {
			p.Rows, p.Truncated, err = textRows(strings.NewReader(t))
		}
	}
	if err != nil {
		return Preview{}, fmt.Errorf("preview %s: %w", id, err)
	}
	return p, nil
}

// csvRows parses RFC 4180 strictly, then retries with LazyQuotes if that fails.
func csvRows(r io.ReadSeeker, comma rune) ([][]string, bool, error) {
	rows, trunc, err := readCSV(r, comma, false)
	if err == nil {
		return rows, trunc, nil
	}
	if _, serr := r.Seek(0, io.SeekStart); serr != nil {
		return nil, false, serr
	}
	rows, trunc, err = readCSV(r, comma, true)
	if err != nil {
		// Still malformed: show what parsed and flag it.
		return rows, true, nil
	}
	return rows, trunc, nil
}

func readCSV(r io.Reader, comma rune, lazy bool) ([][]string, bool, error) {
	br := bufio.NewReader(r)
	if b, _ := br.Peek(3); bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.Comma, cr.LazyQuotes, cr.FieldsPerRecord, cr.ReuseRecord = comma, lazy, -1, false
	rows := [][]string{}
	trunc := false
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return rows, trunc, nil
		}
		if err != nil {
			return rows, trunc, err
		}
		if len(rows) == MaxPreviewRows {
			return rows, true, nil
		}
		if len(rec) > MaxPreviewCols {
			rec, trunc = rec[:MaxPreviewCols], true
		}
		for i := range rec {
			rec[i] = strings.ToValidUTF8(rec[i], "\uFFFD")
		}
		rows = append(rows, rec)
	}
}

// xlsxRows returns sheet names and the display values of one sheet.
func xlsxRows(r io.Reader, sheet int) ([]string, [][]string, bool, error) {
	x, err := excelize.OpenReader(r, xlsxLimits)
	if err != nil {
		return nil, nil, false, fmt.Errorf("open workbook: %w", err)
	}
	defer x.Close()
	sheets := x.GetSheetList()
	rows := [][]string{}
	if sheet < 0 || sheet >= len(sheets) {
		return sheets, rows, false, nil
	}
	it, err := x.Rows(sheets[sheet])
	if err != nil {
		return nil, nil, false, fmt.Errorf("read sheet: %w", err)
	}
	defer it.Close()
	trunc := false
	for it.Next() {
		if len(rows) == MaxPreviewRows {
			return sheets, rows, true, nil
		}
		cols, err := it.Columns()
		if err != nil {
			return nil, nil, false, fmt.Errorf("read row: %w", err)
		}
		if len(cols) > MaxPreviewCols {
			cols, trunc = cols[:MaxPreviewCols], true
		}
		rows = append(rows, cols)
	}
	return sheets, rows, trunc, it.Error()
}

// textRows returns the first MaxPreviewBytes as one-column rows, one per line.
func textRows(r io.Reader) ([][]string, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxPreviewBytes+1))
	if err != nil {
		return nil, false, err
	}
	trunc := len(b) > MaxPreviewBytes
	if trunc {
		b = b[:MaxPreviewBytes]
		for i := 0; i < 3 && len(b) > 0; i++ { // do not split a rune
			if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
				break
			}
			b = b[:len(b)-1]
		}
	}
	lines := strings.Split(clean(string(b)), "\n")
	if len(lines) > MaxPreviewRows {
		lines, trunc = lines[:MaxPreviewRows], true
	}
	rows := make([][]string, len(lines))
	for i, l := range lines {
		rows[i] = []string{strings.TrimSuffix(l, "\r")}
	}
	return rows, trunc, nil
}
