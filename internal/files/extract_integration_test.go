package files

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// minimalPDF builds a valid PDF with one line of text per page.
func minimalPDF(pages ...string) []byte {
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var kids []string
	for _, p := range pages {
		stream := fmt.Sprintf("BT /F1 18 Tf 72 700 Td (%s) Tj ET", p)
		objs = append(objs, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
		objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", len(objs)))
		kids = append(kids, fmt.Sprintf("%d 0 R", len(objs)))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// minimalDOCX builds a Word document with a paragraph and a one-row table.
func minimalDOCX(t *testing.T) []byte {
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hello docx</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExtractorSidecar runs against a real sidecars/extract container:
//
//	docker build -t kera-extract:dev sidecars/extract
//	docker run -d --name kera-extract-test -p 127.0.0.1:18113:8000 kera-extract:dev
//	KERA_TEST_EXTRACT_URL=http://127.0.0.1:18113 go test ./internal/files -run Sidecar
func TestExtractorSidecar(t *testing.T) {
	url := os.Getenv("KERA_TEST_EXTRACT_URL")
	if url == "" {
		t.Skip("KERA_TEST_EXTRACT_URL not set")
	}
	e := setup(t)
	if err := e.a.Settings.Set(context.Background(), "rag.extractor.url", url, false); err != nil {
		t.Fatal(err)
	}
	pdf := e.upload("alice", "two.pdf", minimalPDF("Alpha page", "Beta page"), "")
	f := e.wait(pdf.ID)
	if f.Status != Completed {
		t.Fatalf("pdf: %+v", f)
	}
	if txt := e.text(pdf.ID); !strings.Contains(txt, "Alpha page") || !strings.Contains(txt, "Beta page") || len(f.Meta["pages"].([]any)) != 2 {
		t.Fatalf("pdf text %q meta %v", txt, f.Meta)
	}
	doc := e.upload("alice", "d.docx", minimalDOCX(t), "")
	if f = e.wait(doc.ID); f.Status != Completed {
		t.Fatalf("docx: %+v", f)
	}
	if txt := e.text(doc.ID); txt != "Hello docx\nA1 | B1" {
		t.Fatalf("docx text %q", txt)
	}
	legacy := e.upload("alice", "old.doc", []byte("\xd0\xcf\x11\xe0"), "")
	if f = e.wait(legacy.ID); f.Status != Failed || !strings.Contains(f.Error, "415") {
		t.Fatalf("doc: %+v", f)
	}
}
