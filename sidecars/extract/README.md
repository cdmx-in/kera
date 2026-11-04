# kera-extract

Optional document text extractor for Kera. Kera reads plain text, HTML and
spreadsheets itself and sends PDF, DOCX, PPTX and ODT files here.

```
docker build -t kera-extract sidecars/extract
docker run -d --name kera-extract -p 127.0.0.1:8113:8000 kera-extract
```

Point Kera at it with the runtime setting `rag.extractor.url`
(e.g. `http://kera-extract:8000`). Without it, those uploads end with status
`failed` and the error `no document extractor configured for .pdf`.

## API

- `GET /health` -> `{"status": "ok", "types": [".docx", ".odt", ".pdf", ".pptx"]}`
- `POST /extract`, multipart field `file` ->
  `{"pages": [{"n": 1, "text": "..."}], "meta": {"pages": 1, "title": "..."}}`
  - PDF: one page per PDF page (pypdf text extraction; scanned PDFs have no text).
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
