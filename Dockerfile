FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.22-alpine AS build
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/ ./cmd/toolhub/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/toolhub ./cmd/toolhub
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/toolhub-agent ./cmd/toolhub-agent

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git openssh-client tzdata && addgroup -S toolhub && adduser -S -G toolhub toolhub
WORKDIR /app
COPY --from=build /out/toolhub /usr/local/bin/toolhub
COPY --from=build /out/toolhub-agent /usr/local/bin/toolhub-agent
RUN mkdir -p /data && chown toolhub:toolhub /data
USER toolhub
EXPOSE 18480
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/toolhub"]
