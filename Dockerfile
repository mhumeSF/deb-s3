FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=devel
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/mhumesf/deb-s3-go/internal/buildinfo.Version=${VERSION}" \
    -o /out/deb-s3 ./cmd/deb-s3

FROM alpine:3.24
# gnupg is required only for --sign; ca-certificates for the S3 TLS chain.
RUN apk add --no-cache ca-certificates gnupg \
    && adduser -D -u 65532 deb-s3
COPY --from=build /out/deb-s3 /usr/bin/deb-s3
USER deb-s3
WORKDIR /home/deb-s3
ENTRYPOINT ["deb-s3"]
