package files

import "testing"

func TestExtractable(t *testing.T) {
	for name, want := range map[string]bool{
		"notes.txt":         true,
		"README.MD":         true, // case does not matter
		"main.go":           true,
		"deploy.yaml":       true,
		"dir/sub/query.sql": true,
		"settings.env":      true,
		"page.html":         true,
		"page.htm":          true,
		"sheet.xlsx":        true,
		"macro.xlsm":        true,
		"report.pdf":        true,
		"letter.docx":       true,
		"deck.pptx":         true,
		"old.doc":           true,
		"memo.odt":          true,
		"archive.tar.gz":    false,
		"photo.png":         false,
		"photo.jpg":         false,
		"logo.svg":          false,
		"movie.mp4":         false,
		"binary.exe":        false,
		"Makefile":          false, // no extension
		"report.pdf.bak":    false,
		"old-sheet.xls":     false,
		"":                  false,
		".":                 false,
	} {
		if got := Extractable(name); got != want {
			t.Errorf("Extractable(%q) = %v, want %v", name, got, want)
		}
	}
}
