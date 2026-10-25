# Reuse the locally verified Chromium + Doubletake display base.
ARG DEVICE_BASE=linxi-device-sim:local
FROM golang:1.26.0-trixie AS builder
RUN apt-get update && apt-get install -y --no-install-recommends libopus-dev pkg-config && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/voice ./cmd/voice
COPY cmd/device-display ./cmd/device-display
COPY internal/linx ./internal/linx
COPY internal/voice ./internal/voice
COPY internal/voicecodec ./internal/voicecodec
COPY internal/devicecast ./internal/devicecast
COPY internal/devicecontrol ./internal/devicecontrol
RUN CGO_ENABLED=1 go test -race -tags opus ./internal/... ./cmd/device-display
RUN CGO_ENABLED=1 go build -tags opus -o /out/voice ./cmd/voice
RUN CGO_ENABLED=0 go build -o /out/device-display ./cmd/device-display
FROM ${DEVICE_BASE}
RUN apt-get update && apt-get install -y --no-install-recommends socat libopus0 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/voice /usr/local/bin/voice
COPY --from=builder /out/device-display /usr/local/bin/device-display
COPY deploy/device-sim/start-device.sh /usr/local/bin/start-device
RUN chmod +x /usr/local/bin/start-device
ENV DEVICE_STAGE_URL=http://127.0.0.1:8090/stage/ DEVICE_WAKE_URL=http://127.0.0.1:8788/wake
EXPOSE 8091
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD curl -fsS --max-time 3 http://127.0.0.1:8090/api/status > /dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/start-device"]
