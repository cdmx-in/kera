"""Kera document extractor.

POST /extract (multipart field "file") -> {"pages": [{"n", "text"}], "meta": {"pages", "title"?}}
PDF: one page per PDF page. DOCX/ODT: one page. PPTX: one page per slide.
Plain text and spreadsheets are handled by Kera itself.
"""

import io
import os

from fastapi import FastAPI, File, HTTPException, UploadFile

MAX_BYTES = int(os.environ.get("EXTRACT_MAX_MB", "100")) * 1024 * 1024

app = FastAPI(title="kera-extract", docs_url=None, redoc_url=None)


def pdf_pages(data):
    from pypdf import PdfReader

    reader = PdfReader(io.BytesIO(data))
    if reader.is_encrypted:
        reader.decrypt("")  # many "protected" PDFs only restrict editing
    pages = [{"n": i, "text": (p.extract_text() or "").strip()} for i, p in enumerate(reader.pages, 1)]
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
    if title:
        meta["title"] = title
    return {"pages": pages, "meta": meta}
