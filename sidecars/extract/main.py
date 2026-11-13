"""Kera document extractor.

POST /extract (multipart field "file") -> {"pages": [{"n", "text"}], "meta": {"pages", "title"?}}
PDF: one page per PDF page; a page with no text layer (a scan) is read by OCR when
tesseract and poppler are installed, and meta.ocr_pages counts those pages.
DOCX/ODT: one page. PPTX: one page per slide.
Plain text and spreadsheets are handled by Kera itself.
"""

import io
import logging
import os
import shutil
import subprocess
import tempfile
import time

from fastapi import FastAPI, File, HTTPException, UploadFile

MAX_BYTES = int(os.environ.get("EXTRACT_MAX_MB", "100")) * 1024 * 1024
OCR_MIN_CHARS = 20  # a PDF page with less text than this is treated as an image
OCR_MAX_PAGES = int(os.environ.get("OCR_MAX_PAGES", "100"))  # per file; the rest stay empty
# Kera's client gives up on a request after 10 minutes (about 9 s per dense A4 page on this CPU path):
# stop reading pages before that, so a long scan returns partial text instead of failing.
OCR_BUDGET = int(os.environ.get("OCR_BUDGET_SECONDS", "480"))
OCR_LANGS = os.environ.get("OCR_LANGS", "eng")  # "eng+hin" after installing tesseract-ocr-hin
OCR_DPI = 200
OCR_TIMEOUT = 60  # seconds per page, each of render and recognise
log = logging.getLogger("kera-extract")

app = FastAPI(title="kera-extract", docs_url=None, redoc_url=None)


def ocr_available():
    return bool(shutil.which("tesseract") and shutil.which("pdftoppm"))


def ocr_page(path, n):
    """Text of page n (1-based) of the PDF at path: render it, then recognise it."""
    with tempfile.TemporaryDirectory() as d:
        stem = os.path.join(d, "page")
        subprocess.run(
            ["pdftoppm", "-r", str(OCR_DPI), "-f", str(n), "-l", str(n), "-png", "-singlefile", path, stem],
            check=True, timeout=OCR_TIMEOUT, capture_output=True,
        )
        out = subprocess.run(
            ["tesseract", stem + ".png", "stdout", "-l", OCR_LANGS],
            check=True, timeout=OCR_TIMEOUT, capture_output=True, text=True,
        )
        return out.stdout.strip()


def ocr_blank_pages(data, pages):
    """Fills in pages that have no text layer by OCR. Stops at OCR_MAX_PAGES, after OCR_BUDGET
    seconds or after 3 failures; blank pages not attempted are flagged ocr_skipped."""
    blank = [p for p in pages if len(p["text"]) < OCR_MIN_CHARS]
    if not blank or not ocr_available():
        return
    deadline = time.monotonic() + OCR_BUDGET
    failures = 0
    with tempfile.NamedTemporaryFile(suffix=".pdf") as f:
        f.write(data)
        f.flush()
        for i, p in enumerate(blank):
            if i >= OCR_MAX_PAGES or failures >= 3 or time.monotonic() > deadline:
                log.warning("OCR stopped with %d of %d blank pages unread", len(blank) - i, len(blank))
                for q in blank[i:]:
                    q["ocr_skipped"] = True
                return
            try:
                text = ocr_page(f.name, p["n"])
            except (subprocess.SubprocessError, OSError) as e:
                failures += 1
                log.warning("OCR failed on page %s: %s", p["n"], e)
                continue
            if text:
                p["text"], p["ocr"] = text, True


def pdf_pages(data):
    from pypdf import PdfReader

    reader = PdfReader(io.BytesIO(data))
    if reader.is_encrypted:
        reader.decrypt("")  # many "protected" PDFs only restrict editing
    pages = [{"n": i, "text": (p.extract_text() or "").strip()} for i, p in enumerate(reader.pages, 1)]
    ocr_blank_pages(data, pages)
    title = reader.metadata.title if reader.metadata else None
    return pages, title


def docx_pages(data):
    import docx
    from docx.table import Table
    from docx.text.paragraph import Paragraph

    doc = docx.Document(io.BytesIO(data))
    lines = []
    for child in doc.element.body.iterchildren():
        tag = child.tag.rsplit("}", 1)[-1]
        if tag == "p":
            lines.append(Paragraph(child, doc).text)
        elif tag == "tbl":
            for row in Table(child, doc).rows:
                lines.append(" | ".join(cell.text.strip() for cell in row.cells))
            lines.append("")
    return [{"n": 1, "text": "\n".join(lines).strip()}], doc.core_properties.title


def shape_text(shape):
    if shape.has_text_frame:
        return shape.text_frame.text
    if getattr(shape, "has_table", False) and shape.has_table:
        return "\n".join(" | ".join(c.text for c in row.cells) for row in shape.table.rows)
    if hasattr(shape, "shapes"):  # group shape
        return "\n".join(t for t in (shape_text(s) for s in shape.shapes) if t)
    return ""


def pptx_pages(data):
    from pptx import Presentation

    prs = Presentation(io.BytesIO(data))
    pages = []
    for n, slide in enumerate(prs.slides, 1):
        title = slide.shapes.title
        parts = [title.text_frame.text] if title is not None and title.has_text_frame else []
        for shape in slide.shapes:
            if title is not None and shape.shape_id == title.shape_id:
                continue
            t = shape_text(shape)
            if t.strip():
                parts.append(t)
        if slide.has_notes_slide:
            notes = slide.notes_slide.notes_text_frame.text if slide.notes_slide.notes_text_frame else ""
            if notes.strip():
                parts.append("Notes: " + notes)
        pages.append({"n": n, "text": "\n".join(parts).strip()})
    return pages, prs.core_properties.title


def odt_pages(data):
    from odf import teletype
    from odf.opendocument import load

    doc = load(io.BytesIO(data))
    lines = []

    def walk(node):
        for child in node.childNodes:
            if getattr(child, "tagName", "") in ("text:p", "text:h"):
                lines.append(teletype.extractText(child))
            else:
                walk(child)

    walk(doc.text)
    return [{"n": 1, "text": "\n".join(lines).strip()}], None


HANDLERS = {".pdf": pdf_pages, ".docx": docx_pages, ".pptx": pptx_pages}
try:
    import odf  # noqa: F401  optional

    HANDLERS[".odt"] = odt_pages
except ImportError:
    pass


@app.get("/health")
def health():
    return {"status": "ok", "types": sorted(HANDLERS)}


@app.post("/extract")
def extract(file: UploadFile = File(...)):
    ext = os.path.splitext((file.filename or "").lower())[1]
    handler = HANDLERS.get(ext)
    if handler is None:
        hint = " (save it as .docx)" if ext == ".doc" else ""
        raise HTTPException(415, f"unsupported type {ext or file.content_type or 'unknown'}{hint}")
    data = file.file.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        raise HTTPException(413, f"file larger than {MAX_BYTES // (1024 * 1024)} MB")
    try:
        pages, title = handler(data)
    except Exception as e:  # corrupt or password-protected files
        raise HTTPException(422, f"could not read {ext} file: {e}") from e
    meta = {"pages": len(pages)}
    if ocr := sum(1 for p in pages if p.get("ocr")):
        meta["ocr_pages"] = ocr
    if skipped := sum(1 for p in pages if p.get("ocr_skipped")):
        meta["ocr_skipped"] = skipped  # blank pages left unread (page cap, time budget or failures)
    if title:
        meta["title"] = title
    return {"pages": pages, "meta": meta}
