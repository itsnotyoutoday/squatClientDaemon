# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/squatterd ./cmd/squatterd && mkdir /out/data

FROM scratch
COPY --from=build /out/squatterd /squatterd
COPY --from=build --chown=65532:65532 /out/data /data
# The data dir (device keys, TLS key, session history) must be a volume.
VOLUME /data
EXPOSE 10327
USER 65532:65532
ENTRYPOINT ["/squatterd"]
CMD ["serve", "--data-dir", "/data", "--bind", "0.0.0.0", "--port", "10327"]
