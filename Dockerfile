# Stage 1: Build stage
FROM golang:1.25 AS builder
WORKDIR /home/metrics/
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o hydrolix-collector .

# Stage 2: Runtime stage
FROM alpine:3.21
WORKDIR /root/
COPY --from=builder /home/metrics/hydrolix-collector .
CMD ["./hydrolix-collector"]