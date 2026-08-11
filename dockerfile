# Multi-stage build.
#
# The original was a single stage on golang:latest, which shipped the whole Go
# toolchain and every source file in the runtime image — roughly 800MB.
FROM golang:1.24-alpine AS build
WORKDIR /src

# Copy the manifests first so `go mod download` is cached independently of the
# source: an edit to a .go file then does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs on a distroless base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webscraper .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/webscraper /webscraper

ENV GIN_MODE=release
EXPOSE 8081
USER nonroot:nonroot

# ALLOW_PRIVATE_HOSTS is deliberately NOT set here. Left unset the SSRF guard
# is on, which is the only safe default for a service that fetches whatever URL
# it is given.
ENTRYPOINT ["/webscraper"]
