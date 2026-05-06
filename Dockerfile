# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.26.2
ARG ALPINE_VERSION=3.20

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS builder

ENV CGO_ENABLED=0 \
    GO111MODULE=on

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
        -trimpath \
        -ldflags "-s -w \
            -X github.com/compozy/skeeper/internal/version.Version=${VERSION} \
            -X github.com/compozy/skeeper/internal/version.Commit=${COMMIT} \
            -X github.com/compozy/skeeper/internal/version.BuildDate=${BUILD_DATE}" \
        -o /out/skeeper \
        ./cmd/skeeper

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=builder /out/skeeper /usr/local/bin/skeeper
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/skeeper"]
CMD ["run"]
