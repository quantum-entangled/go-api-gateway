FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN go build -o /gateway ./cmd/gateway


FROM alpine:3.23

COPY --from=build /gateway /gateway

ENTRYPOINT ["/gateway"]
