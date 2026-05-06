FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./

RUN go mod download

COPY internal/ ./internal/
COPY cmd/ ./cmd/

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/gateway


FROM alpine:3.23 AS dev

COPY --from=build /gateway /gateway

EXPOSE 8080

ENTRYPOINT ["/gateway"]


FROM gcr.io/distroless/static-debian12:nonroot AS prod

COPY --from=build /gateway /gateway
COPY --from=busybox:1.37.0-musl /bin/wget /usr/bin/wget

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/gateway"]
