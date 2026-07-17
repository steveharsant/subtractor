# --- Build stage -------------------------------------------------------------
FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web

RUN go vet ./... && go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subtractor .

# --- Runtime stage ------------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ffmpeg \
    && adduser -D -H -u 10001 subtractor \
    && mkdir -p /data \
    && chown subtractor:subtractor /data

COPY --from=build /out/subtractor /usr/local/bin/subtractor

USER subtractor
VOLUME ["/data"]
EXPOSE 8080

# Required at runtime: SUBTRACTOR_PASSWORD (or SUBTRACTOR_PASSWORD_FILE).
# Optional: SUBTRACTOR_USERNAME (default: admin), SUBTRACTOR_RETENTION_DAYS (14),
#           SUBTRACTOR_MAX_UPLOAD_MB (8192), SUBTRACTOR_SECURE_COOKIES (false),
#           SUBTRACTOR_LISTEN (:8080), SUBTRACTOR_DATA_DIR (/data).
ENTRYPOINT ["subtractor"]
