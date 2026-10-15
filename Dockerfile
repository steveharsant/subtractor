# --- Build stage -------------------------------------------------------------
FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web

RUN go vet ./... && go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subtractor .

# --- whisper.cpp build stage --------------------------------------------------
# Speech-to-text engine. Built from a pinned release tag. GGML_CPU_ALL_VARIANTS
# compiles one CPU backend per instruction-set level (SSE4.2 ... AVX-512) and
# the fastest one the host supports is loaded at runtime, so the image stays
# portable without giving up AVX2/AVX-512 speed.
FROM alpine:3.20 AS whisper

ARG WHISPER_CPP_VERSION=v1.9.4
RUN apk add --no-cache build-base cmake git
RUN git clone --depth 1 --branch ${WHISPER_CPP_VERSION} https://github.com/ggml-org/whisper.cpp /src/whisper.cpp
WORKDIR /src/whisper.cpp
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
      -DGGML_NATIVE=OFF -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON \
      -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF -DWHISPER_SDL2=OFF \
    && cmake --build build -j"$(nproc)" --target whisper-cli \
    && mkdir -p /opt/whisper \
    && cp -P build/bin/whisper-cli build/bin/*.so* /opt/whisper/

# --- Runtime stage ------------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ffmpeg libstdc++ libgomp \
    && adduser -D -H -u 10001 subtractor \
    && mkdir -p /data \
    && chown subtractor:subtractor /data

COPY --from=build /out/subtractor /usr/local/bin/subtractor
COPY --from=whisper /opt/whisper /opt/whisper
ENV PATH="/opt/whisper:${PATH}" \
    LD_LIBRARY_PATH=/opt/whisper

USER subtractor
VOLUME ["/data"]
EXPOSE 8080

# Required at runtime: SUBTRACTOR_PASSWORD (or SUBTRACTOR_PASSWORD_FILE).
# Optional: SUBTRACTOR_USERNAME (default: admin), SUBTRACTOR_RETENTION_DAYS (14),
#           SUBTRACTOR_MAX_UPLOAD_MB (8192), SUBTRACTOR_SECURE_COOKIES (false),
#           SUBTRACTOR_LISTEN (:8080), SUBTRACTOR_DATA_DIR (/data),
#           SUBTRACTOR_WHISPER_MODEL (auto), SUBTRACTOR_WHISPER_LANGUAGE (auto),
#           SUBTRACTOR_WHISPER_THREADS (auto).
# The Whisper model is downloaded into /data/models on first start.
ENTRYPOINT ["subtractor"]
