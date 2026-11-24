# kera-extract

Document text extractor for Kera. Kera reads plain text, HTML and spreadsheets itself and sends PDF, DOCX,
PPTX and ODT files here. It is the `extract` service of the Docker Compose stack in `deploy/`, which pulls
`ghcr.io/cdmx-in/kera-extract` (amd64 and arm64) or builds this directory, and tells Kera where it is, so
there is nothing to set up. See [Document extraction](../../docs/deploy.md#document-extraction) for memory,
turning it off, more OCR languages and what an upload says when the service is down.

Without Compose:

```
docker build -t kera-extract sidecars/extract
docker run -d --name kera-extract -p 127.0.0.1:8113:8000 kera-extract
```

and point Kera at it with the environment variable `KERA_EXTRACTOR_URL` (the default) or the runtime setting
`rag.extractor.url` (Admin > Settings > Retrieval), e.g. `http://kera-extract:8000` from another container on
the same network. A saved setting wins over the variable. With neither, those uploads end with status `failed`
and the error `no document extractor configured for .pdf`.

## Settings

Environment variables of the container:

| Variable | Default | Meaning |
|---|---|---|
| `WEB_CONCURRENCY` | `2` (the compose file sets `1`) | uvicorn workers, about 45 MB of memory each when idle |
| `EXTRACT_MAX_MB` | `100` | largest upload; bigger files answer `413` |
| `OCR_LANGS` | `eng` | tesseract languages, `eng+deu`; each needs its `tesseract-ocr-<code>` package in the image |
| `OCR_MAX_PAGES` | `100` | pages OCR'd per file |
| `OCR_BUDGET_SECONDS` | `480` | OCR time per file (Kera's client gives up after 10 minutes) |

## API

- `GET /health` -> `{"status": "ok", "types": [".docx", ".odt", ".pdf", ".pptx"], "ocr": true, "ocr_langs": "eng"}`
  (`ocr` is false when tesseract or `pdftoppm` is missing from the image; Admin > Settings > Retrieval shows it)
- `POST /extract`, multipart field `file` ->
  `{"pages": [{"n": 1, "text": "..."}], "meta": {"pages": 1, "title": "..."}}`
  - PDF: one page per PDF page (pypdf text extraction). A page with no text layer (a scan) is read by
    OCR (tesseract, CPU, English; `OCR_LANGS=eng+hin` after installing `tesseract-ocr-hin`, see the deploy docs for a compose recipe). `meta.ocr_pages`
    counts pages read that way (OCR text can contain errors) and `meta.ocr_skipped` blank pages left unread
    after `OCR_MAX_PAGES` (100), `OCR_BUDGET_SECONDS` (480; Kera's client gives up after 10 minutes) or three
    failures. Without tesseract and `pdftoppm` in the image, scans stay empty.
  - DOCX: one page with paragraphs and tables (`cell | cell`) in document order.
  - PPTX: one page per slide: title, text frames, tables, then `Notes: ...`.
  - ODT: one page (needs `odfpy`, installed by default).
  - Errors: `415` unsupported type (legacy `.doc` included: save it as `.docx`),
    `413` larger than `EXTRACT_MAX_MB` (default 100), `422` unreadable file.
    Bodies are `{"detail": "..."}`.

Any service with the same contract can replace this one.

## Tests

```
docker run --rm -v "$PWD/sidecars/extract":/src:ro -w /src -u 0 kera-extract \
  sh -c "pip install -q -r requirements-dev.txt && python -m pytest -q -p no:cacheprovider"
```

The Go side has an integration test that runs when `KERA_TEST_EXTRACT_URL` is set
(see `internal/files/extract_integration_test.go`).
