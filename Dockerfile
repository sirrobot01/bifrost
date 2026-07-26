FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN if [ "$TARGETARCH" = arm ]; then export GOARM="${TARGETVARIANT#v}"; fi; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/bifrost ./cmd/bifrost

FROM --platform=$BUILDPLATFORM alpine:3.24 AS certificates
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/bifrost /bifrost
USER 65532:65532
ENTRYPOINT ["/bifrost"]
