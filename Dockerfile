FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/hcb-mcp ./cmd/hcb-mcp \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/hcb ./cmd/hcb

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /out/hcb-mcp /usr/local/bin/hcb-mcp
COPY --from=build /out/hcb /usr/local/bin/hcb

# Credentials live on a persistent volume so rotated refresh tokens survive
# restarts. Seed via HCB_CREDENTIALS_JSON on first boot; never baked in.
ENV HCB_CREDENTIALS=/data/credentials.json
EXPOSE 8080
CMD ["hcb-mcp", "--http", ":8080"]
