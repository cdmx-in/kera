FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /app
# OCR for scanned PDFs (CPU only): tesseract with English data, poppler's pdftoppm renders the pages.
# One OpenMP thread per tesseract: the two uvicorn workers already use the CPUs.
RUN apt-get update && apt-get install -y --no-install-recommends tesseract-ocr poppler-utils \
    && rm -rf /var/lib/apt/lists/*
ENV OMP_THREAD_LIMIT=1
COPY requirements.txt .
RUN pip install -r requirements.txt && useradd --system --uid 10001 --no-create-home extract
COPY main.py .
USER 10001
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health')"]
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "2"]
