FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/broker ./cmd/broker

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/broker /broker
EXPOSE 8080 7070
USER nonroot:nonroot
ENTRYPOINT ["/broker"]
