FROM debian:stable-slim
ENV DEBIAN_FRONTEND=noninteractive PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 PATH=/opt/venv/bin:$PATH
# x11vnc and websockify serve the live view (x11vnc per session, websockify on 6080, websocket only:
# Kera bundles the noVNC client, so the novnc package and its nodejs are not needed).
RUN apt-get update && apt-get install -y --no-install-recommends \
      xvfb xauth openbox xdotool x11-utils firefox-esr fonts-dejavu-core fonts-noto-core \
      x11vnc websockify tini ca-certificates python3 python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    # every window maximized and undecorated, so screenshot pixels map to the page 1:1
    && sed -i 's|<applications>|<applications><application class="*"><decor>no</decor><maximized>yes</maximized></application>|' \
         /etc/xdg/openbox/rc.xml \
    && grep -q 'class="\*"' /etc/xdg/openbox/rc.xml \
    && useradd --uid 1000 --create-home --home-dir /home/desk --shell /bin/bash desk \
    && install -d -o desk -g desk -m 700 /home/desk/sessions
WORKDIR /app
COPY requirements.txt .
RUN python3 -m venv /opt/venv && pip install -r requirements.txt
COPY main.py .
USER 1000
EXPOSE 8000 6080
HEALTHCHECK --interval=30s --timeout=3s CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health')"]
# One worker: the sessions (X displays, processes) live in this process.
ENTRYPOINT ["tini", "--"]
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1"]
