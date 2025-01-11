FROM golang:1-alpine AS builder

RUN apk add --no-cache ca-certificates
WORKDIR /build/gomuks-push
COPY . /build/gomuks-push
ENV CGO_ENABLED=0
RUN go build -o /usr/bin/gomuks-push

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/bin/gomuks-push /usr/bin/gomuks-push

ENTRYPOINT ["/usr/bin/gomuks-push"]
