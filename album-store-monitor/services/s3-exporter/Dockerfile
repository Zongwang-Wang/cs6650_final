FROM python:3.12-alpine
RUN pip install --no-cache-dir boto3
WORKDIR /app
COPY exporter.py .
EXPOSE 9399
ENTRYPOINT ["python3", "exporter.py"]
