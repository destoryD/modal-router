FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o modals-router .

FROM python:3.12-slim

# Install system libraries required by Camoufox/Firefox
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata \
    libgtk-3-0 libasound2 libdbus-glib-1-2 libx11-xcb1 \
    libxcb-shm0 libxcomposite1 libxdamage1 libxrandr2 \
    libatk1.0-0 libatk-bridge2.0-0 libpango-1.0-0 \
    libcairo2 libgdk-pixbuf-2.0-0 libxfixes3 libxkbcommon0 \
    libxrender1 libxtst6 libnss3 libnspr4 libgbm1 \
    fonts-liberation xvfb dbus \
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
ENV CAMOUFOX_OS=linux
ENV MOZ_DISABLE_CONTENT_SANDBOX=1
ENV FIREFOX_DISABLE_CONTENT_SANDBOX=1
CMD ["./modals-router"]
