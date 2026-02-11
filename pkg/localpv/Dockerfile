# ====================================================================
# LocalPV CSI Driver Dockerfile
# ====================================================================

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN set -e; \
    if [ -n "${TARGETOS}" ] && [ -n "${TARGETARCH}" ]; then \
        echo "building for ${TARGETOS}/${TARGETARCH}"; \
        CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -ldflags="-w -s" -o /bin/localpv-csi ./pkg/localpv/cmd/localpv-csi; \
    else \
        echo "building for native platform"; \
        CGO_ENABLED=0 go build -ldflags="-w -s" -o /bin/localpv-csi ./pkg/localpv/cmd/localpv-csi; \
    fi

FROM gcr.io/distroless/static:debug

USER 0:0

COPY --from=builder /bin/localpv-csi /localpv-csi

ENTRYPOINT ["/localpv-csi"]
