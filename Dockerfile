FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /guard .
# The secrets server, in the same image and deployed as its own container. One
# image because they are one codebase and one schema; two containers because the
# entire point is that guard can be restarted, rolled back or broken without an
# application losing the configuration it boots with.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /guard-vault ./cmd/vault

FROM alpine:3.22
RUN addgroup -S guard && adduser -S -G guard guard && mkdir -p /data && chown guard:guard /data
COPY --from=build /guard /usr/local/bin/guard
COPY --from=build /guard-vault /usr/local/bin/guard-vault
USER guard
ENV GUARD_DB_PATH=/data/guard.db
VOLUME ["/data"]
EXPOSE 4318
ENTRYPOINT ["guard"]
