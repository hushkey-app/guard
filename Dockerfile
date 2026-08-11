FROM node:24-alpine AS assets
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY client/styles ./client/styles
COPY client/pages ./client/pages
COPY client/ui ./client/ui
COPY client/public/guard.js ./client/public/guard.js
RUN npm run css:build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=assets /src/client/public/app.css ./client/public/app.css
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /guard .

FROM alpine:3.22
RUN addgroup -S guard && adduser -S -G guard guard && mkdir -p /data && chown guard:guard /data
COPY --from=build /guard /usr/local/bin/guard
USER guard
ENV GUARD_DB_PATH=/data/guard.db
VOLUME ["/data"]
EXPOSE 4318
ENTRYPOINT ["guard"]
