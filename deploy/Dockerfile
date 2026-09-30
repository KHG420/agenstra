FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="$GOPROXY" go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agenstra-serve ./cmd/agenstra-serve

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S agent && adduser -S -G agent -u 10001 agent && mkdir -p /data /etc/agent && chown agent:agent /data
COPY --from=build /out/agenstra-serve /usr/local/bin/agenstra-serve
ENV AGENT_DEPLOYMENT_CONFIG=/etc/agent/deployment.json
USER agent
VOLUME /data
EXPOSE 8091
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD wget -q -O /dev/null http://127.0.0.1:8091/readyz || exit 1
CMD ["agenstra-serve", "--host", "0.0.0.0", "--port", "8091"]
