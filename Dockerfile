FROM golang:1.25.5 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN make build

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/build/vercel-proxy /main
EXPOSE 3000
ENTRYPOINT ["/main"]
CMD ["--addr", ":3000"]
