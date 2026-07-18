# Multi-stage build: compile with the Go toolchain, ship a tiny final image.
# The resulting image is a few megabytes and contains no shell, no package
# manager and nothing else to attack.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/gobalance ./cmd/gobalance

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/gobalance /gobalance
COPY gobalance.yaml /etc/gobalance/gobalance.yaml
EXPOSE 8080 8081
ENTRYPOINT ["/gobalance", "-config", "/etc/gobalance/gobalance.yaml"]
