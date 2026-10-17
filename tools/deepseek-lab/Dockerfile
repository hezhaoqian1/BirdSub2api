FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY app.py engine.py transport.py egress.py security.py suites.py README.md ./
COPY static ./static
COPY data ./data
CMD ["python", "app.py"]
