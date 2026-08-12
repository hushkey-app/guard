FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /guard .

FROM alpine:3.22
RUN addgroup -S guard && adduser -S -G guard guard && mkdir -p /data && chown guard:guard /data
COPY --from=build /guard /usr/local/bin/guard
USER guard
ENV GUARD_DB_PATH=/data/guard.db
VOLUME ["/data"]
EXPOSE 4318
ENTRYPOINT ["guard"]
