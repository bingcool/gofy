FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=1.0.0
ARG COMMIT=none
ARG BUILD_AT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_AT}" \
    -o /out/demo-service .

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/demo-service /app/demo-service
COPY config.yaml /app/config.yaml
EXPOSE 8080
ENTRYPOINT ["/app/demo-service"]
