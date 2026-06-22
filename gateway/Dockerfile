FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY gateway/go.mod gateway/go.sum ./
RUN go mod download

COPY gateway/ .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gateway .

# ---

FROM python:3.11-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        libgl1 \
        libglib2.0-0 \
        ca-certificates \
        tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY --from=builder /app/gateway .

COPY crop_counter.py .
COPY yolo_detector.py .
COPY models/ ./models/

EXPOSE 8080

CMD ["./gateway"]
