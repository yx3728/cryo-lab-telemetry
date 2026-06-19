# Multi-stage build for the Go API. Builds natively for the host arch, so on the
# Apple-Silicon dev machine and the t4g (ARM) EC2 box it produces arm64 images.

# --- build stage -------------------------------------------------------------
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache dependencies first for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO disabled => a static binary that runs on a scratch/distroless base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# --- runtime stage -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
