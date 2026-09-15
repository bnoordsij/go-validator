FROM golang:1.25-bookworm AS builder

WORKDIR /app

COPY go.* ./
RUN go mod download

COPY . ./

RUN go build -v -o server

CMD ["/app/server"]

