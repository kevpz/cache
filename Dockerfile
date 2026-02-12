FROM golang:1.24-alpine AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /cache ./cmd/

FROM alpine:3.20
COPY --from=build /cache /cache
ENV GOMEMLIMIT=30GiB
ENTRYPOINT ["/cache"]
