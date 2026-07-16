# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS="$TARGETOS" GOARCH="$TARGETARCH" ./build.sh

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /src/bin/anyssh-server /anyssh-server
ENV ANYSSH_LISTEN=:8080
EXPOSE 8080
ENTRYPOINT ["/anyssh-server"]
