# The collector as a container — used for the optional cloud "mockfeed" demo
# producer. On the real lab PC the collector runs directly on Python (no Docker),
# reusing the existing PyVISA code; see WIRING.md.
FROM python:3.12-slim
WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .

# Mock reader by default; override env to point at a real ingest endpoint.
ENV READER=mock \
    BUFFER_DIR=/app/buffer
ENTRYPOINT ["python", "collector.py"]
