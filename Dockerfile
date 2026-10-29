# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html vite.config.ts tsconfig.json ./
COPY src ./src
COPY public ./public
COPY shared ./shared
RUN VITE_API_URL= npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION -X main.revision=$REVISION" -o /trending ./cmd/trending

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -u 10001 -G app app
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/lizhian/github-trending-tracking" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION
WORKDIR /app
COPY --from=backend /trending /app/trending
COPY --from=frontend /src/dist /app/static
ENV DATA_DIR=/data STATIC_DIR=/app/static LISTEN_ADDR=:8080
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/trending"]
CMD ["serve"]
