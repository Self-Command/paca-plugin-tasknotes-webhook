FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_SHA
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test "${#SOURCE_SHA}" -eq 40 && printf 'package buildinfo\nfunc init() { SourceSHA = "%s" }\n' "$SOURCE_SHA" > internal/buildinfo/stamp.go
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /worker ./cmd/worker
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /worker /worker
ENTRYPOINT ["/worker"]
