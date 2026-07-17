FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kubesqueeze ./cmd/kubesqueeze

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S kubesqueeze && adduser -S -G kubesqueeze kubesqueeze
WORKDIR /app
COPY --from=go /out/kubesqueeze /app/kubesqueeze
COPY --from=web /src/web/dist /app/web
USER kubesqueeze
EXPOSE 8080 8081
ENTRYPOINT ["/app/kubesqueeze"]
CMD ["server"]
