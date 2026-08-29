FROM golang:1.24-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY internal/ ./internal/
COPY web/ ./web/
COPY data/templates/ ./data/templates/
COPY data/students.example.csv ./data/students.example.csv

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/classroom-agent . \
    && mkdir -p /out/data \
    && cp data/students.example.csv /out/data/students.csv

FROM alpine:3.22

WORKDIR /app

ENV LISTEN_ADDR=:8080 \
    DATABASE_PATH=/data/app.db \
    STUDENTS_CSV=/data/students.csv

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S classroom \
    && adduser -S -D -H -u 10001 -G classroom classroom \
    && mkdir -p /data \
    && chown classroom:classroom /data

COPY --from=builder --chown=classroom:classroom /out/classroom-agent /app/classroom-agent
COPY --from=builder --chown=classroom:classroom /out/data/ /data/

VOLUME ["/data"]
EXPOSE 8080

USER classroom
ENTRYPOINT ["/app/classroom-agent"]
