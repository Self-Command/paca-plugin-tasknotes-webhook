FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /worker ./cmd/worker
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /worker /worker
ENTRYPOINT ["/worker"]
