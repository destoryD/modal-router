FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o modals-router .

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/modals-router .
EXPOSE 8080
VOLUME /app/data
ENV ROUTER_LISTEN=:8080
ENV ROUTER_DATA_DIR=/app/data
ENV ROUTER_MAX_RETRIES=3
CMD ["./modals-router"]
