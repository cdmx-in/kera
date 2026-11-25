# The runner for Kera-run pipelines: pulls queued runs from Kera and executes the stored script. It needs a shell,
# python and the usual tools, which the Kera image (distroless) does not have; the `kera` binary is copied in from it.
#   docker build -f sidecars/runner/Dockerfile --build-arg KERA_IMAGE=ghcr.io/cdmx-in/kera:latest -t kera-runner sidecars/runner
ARG KERA_IMAGE=kera:local
FROM ${KERA_IMAGE} AS kera

FROM python:3.12-slim
LABEL org.opencontainers.image.source="https://github.com/cdmx-in/kera" org.opencontainers.image.description="Script runner for Kera pipelines"
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1 TZ=UTC
# what the existing knowledge-base scripts use: bash, git (archive/log/rev-parse), tar, diff, find, curl, sqlite3, ssh for private repos
RUN apt-get update && apt-get install -y --no-install-recommends bash git tar gzip diffutils findutils curl ca-certificates sqlite3 openssh-client tini \
    && rm -rf /var/lib/apt/lists/*
COPY --from=kera /usr/local/bin/kera /usr/local/bin/kera
# /work holds one folder per pipeline (the workspace a script keeps its state in) and .venvs (packages, cached by requirements.txt)
RUN mkdir -p /work && chmod 0777 /work
WORKDIR /work
ENV KERA_RUNNER_WORK=/work
# no inbound port: the runner only calls Kera. It runs as the user the compose file names, because the folders it mounts have owners.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/kera"]
CMD ["runner"]
