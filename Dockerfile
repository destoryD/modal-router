FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o modals-router .

FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir camoufox[geoip] playwright

RUN python -m camoufox fetch

WORKDIR /app
COPY --from=builder /build/modals-router .
COPY --from=builder /build/internal/modalauth/modal_login.py /app/modal_login_ref.py

EXPOSE 8080
VOLUME /app/data
ENV ROUTER_LISTEN=:8080
ENV ROUTER_DATA_DIR=/app/data
ENV ROUTER_MAX_RETRIES=3
ENV MODAL_PYTHON=python
CMD ["./modals-router"]
