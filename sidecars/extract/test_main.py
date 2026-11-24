import io

from fastapi.testclient import TestClient

import main

client = TestClient(main.app)


def make_pdf(pages):
    """A minimal valid PDF with one line of Helvetica text per page."""
    objs = ["<< /Type /Catalog /Pages 2 0 R >>", None, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    kids = []
    for text in pages:
        stream = f"BT /F1 18 Tf 72 700 Td ({text}) Tj ET"
        objs.append(f"<< /Length {len(stream)} >>\nstream\n{stream}\nendstream")
        content = len(objs)
        objs.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents {content} 0 R "
                    "/Resources << /Font << /F1 3 0 R >> >> >>")
        kids.append(f"{len(objs)} 0 R")
    objs[1] = f"<< /Type /Pages /Kids [{' '.join(kids)}] /Count {len(kids)} >>"
    out, offsets = b"%PDF-1.4\n", []
    for i, o in enumerate(objs, 1):
        offsets.append(len(out))
        out += f"{i} 0 obj\n{o}\nendobj\n".encode()
    xref = len(out)
    out += f"xref\n0 {len(objs) + 1}\n0000000000 65535 f \n".encode()
    out += "".join(f"{o:010d} 00000 n \n" for o in offsets).encode()
    out += f"trailer\n<< /Size {len(objs) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode()
    return out


def post(name, data):
    return client.post("/extract", files={"file": (name, data)})


def test_health():
    r = client.get("/health")
    body = r.json()
    assert r.status_code == 200 and ".pdf" in body["types"]
    assert body["ocr"] == main.ocr_available() and body["ocr_langs"] == main.OCR_LANGS


def test_pdf_one_page_per_page():
    r = post("a.pdf", make_pdf(["Hello one", "Second page"]))
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["meta"]["pages"] == 2
    assert [p["n"] for p in body["pages"]] == [1, 2]
    assert "Hello one" in body["pages"][0]["text"] and "Second page" in body["pages"][1]["text"]


def test_docx_paragraphs_and_tables():
    import docx

    d = docx.Document()
    d.core_properties.title = "Policy"
    d.add_paragraph("First paragraph")
    t = d.add_table(rows=1, cols=2)
    t.cell(0, 0).text, t.cell(0, 1).text = "A1", "B1"
    d.add_paragraph("After table")
    buf = io.BytesIO()
    d.save(buf)
    body = post("p.docx", buf.getvalue()).json()
    assert body["meta"] == {"pages": 1, "title": "Policy"}
    text = body["pages"][0]["text"]
    assert text.index("First paragraph") < text.index("A1 | B1") < text.index("After table")


def test_pptx_slides_titles_and_notes():
    from pptx import Presentation

    prs = Presentation()
    for i in (1, 2):
        s = prs.slides.add_slide(prs.slide_layouts[1])
        s.shapes.title.text = f"Slide {i}"
        s.placeholders[1].text = f"Body {i}"
        s.notes_slide.notes_text_frame.text = f"Note {i}"
    buf = io.BytesIO()
    prs.save(buf)
    body = post("deck.pptx", buf.getvalue()).json()
    assert [p["n"] for p in body["pages"]] == [1, 2]
    assert body["pages"][1]["text"] == "Slide 2\nBody 2\nNotes: Note 2"


def test_odt():
    from odf.opendocument import OpenDocumentText
    from odf.text import H, P

    doc = OpenDocumentText()
    doc.text.addElement(H(outlinelevel=1, text="Title"))
    doc.text.addElement(P(text="Body text"))
    buf = io.BytesIO()
    doc.write(buf)
    body = post("x.odt", buf.getvalue()).json()
    assert body["pages"][0]["text"] == "Title\nBody text"


def test_unsupported_and_corrupt():
    r = post("x.doc", b"\xd0\xcf\x11\xe0")
    assert r.status_code == 415 and ".doc" in r.json()["detail"]
    assert post("x.exe", b"MZ").status_code == 415
    assert post("bad.pdf", b"not a pdf").status_code == 422


def test_size_cap(monkeypatch):
    monkeypatch.setattr(main, "MAX_BYTES", 10)
    assert post("a.pdf", b"x" * 11).status_code == 413


def test_ocr_only_blank_pages_capped_and_counted(monkeypatch):
    asked = []
    monkeypatch.setattr(main, "ocr_available", lambda: True)
    monkeypatch.setattr(main, "ocr_page", lambda path, n: asked.append(n) or f"scanned {n}")
    monkeypatch.setattr(main, "OCR_MAX_PAGES", 2)
    # page 1 has a text layer; pages 2-4 are blank, only two of them are read
    body = post("scan.pdf", make_pdf(["A text layer long enough to keep", "", "", ""])).json()
    assert asked == [2, 3]
    assert [p["text"] for p in body["pages"]][1:] == ["scanned 2", "scanned 3", ""]
    assert body["pages"][0]["text"].startswith("A text layer") and "ocr" not in body["pages"][0]
    assert body["meta"]["ocr_pages"] == 2 and body["meta"]["ocr_skipped"] == 1  # the third blank page


def test_ocr_time_budget_returns_partial_text(monkeypatch):
    monkeypatch.setattr(main, "ocr_available", lambda: True)
    monkeypatch.setattr(main, "ocr_page", lambda path, n: "never read")
    monkeypatch.setattr(main, "OCR_BUDGET", -1)  # already over budget
    body = post("scan.pdf", make_pdf(["", ""])).json()
    assert body["meta"]["ocr_skipped"] == 2 and "ocr_pages" not in body["meta"] and body["pages"][0]["text"] == ""


def test_ocr_stops_after_three_failures_and_without_tools(monkeypatch):
    calls = []

    def boom(path, n):
        calls.append(n)
        raise main.subprocess.SubprocessError("no language data")

    monkeypatch.setattr(main, "ocr_available", lambda: True)
    monkeypatch.setattr(main, "ocr_page", boom)
    body = post("scan.pdf", make_pdf([""] * 6)).json()
    assert len(calls) == 3 and "ocr_pages" not in body["meta"]  # still a valid, empty extraction
    assert body["meta"]["ocr_skipped"] == 3

    calls.clear()
    monkeypatch.setattr(main, "ocr_available", lambda: False)
    assert post("scan.pdf", make_pdf([""])).status_code == 200 and not calls


def test_ocr_reads_a_real_scan():
    import pytest

    if not main.ocr_available():
        pytest.skip("tesseract and pdftoppm are not installed")
    Image = pytest.importorskip("PIL.Image")
    from PIL import ImageDraw, ImageFont

    img = Image.new("RGB", (1400, 300), "white")
    ImageDraw.Draw(img).text((40, 80), "INVOICE TOTAL 4210", fill="black", font=ImageFont.load_default(size=90))
    buf = io.BytesIO()
    img.save(buf, "PDF")  # an image-only PDF: no text layer
    body = post("scan.pdf", buf.getvalue()).json()
    assert body["meta"]["ocr_pages"] == 1
    assert "INVOICE" in body["pages"][0]["text"].upper() and "4210" in body["pages"][0]["text"]
