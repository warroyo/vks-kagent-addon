# The bootstrap Job's image. Build from this directory:
#   docker build -t ghcr.io/warroyo/vks-kagent-addon/bootstrap:0.1.5 .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bootstrap .

# A static binary needs no libc. The nonroot user is numeric (65532), which the
# chart's runAsNonRoot requires.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /bootstrap /bootstrap
USER 65532:65532
ENTRYPOINT ["/bootstrap"]
