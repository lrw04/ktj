FROM golang:1.20 as builder
WORKDIR /app
COPY . .
RUN go build .

FROM scratch
WORKDIR /etc/ktj
COPY --from=builder /app/ktj /app/ktj

ENTRYPOINT ["/app/ktj"]
