# InfluxDB -> AWS forwarder (runs on the EC2 box next to the API).
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY forwarder.py .
ENTRYPOINT ["python", "forwarder.py"]
