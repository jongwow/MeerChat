# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/jongwow/meerchat/internal/version.version=${VERSION}" \
    -o /out/meer ./cmd/meer
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/meer /usr/local/bin/meer
COPY --from=build --chown=nonroot:nonroot /out/data /data
EXPOSE 6337
VOLUME /data
ENTRYPOINT ["/usr/local/bin/meer"]
CMD ["serve", "--db", "/data/relay.db"]
