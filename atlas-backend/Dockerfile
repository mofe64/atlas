# Build in a full Go image, then copy only the static binary into the runtime image.
# Keeping compilers out of the final image reduces both size and attack surface.
FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/atlas-backend ./cmd/atlas-backend

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S atlas \
    && adduser -S -G atlas atlas

COPY --from=build /out/atlas-backend /usr/local/bin/atlas-backend

USER atlas
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/atlas-backend"]
