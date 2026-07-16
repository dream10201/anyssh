# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS="$TARGETOS" GOARCH="$TARGETARCH" ./build.sh && mkdir -p /src/empty-data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /src/bin/anyssh-server /anyssh-server
COPY --from=build --chown=65532:65532 /src/empty-data /data
ENV ANYSSH_LISTEN=:8080 ANYSSH_DATA_FILE=/data/state.json
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/anyssh-server"]
