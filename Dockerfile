FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
# VERSION is wired into pkg/version.Version via -ldflags so the running
# controller logs its image tag at boot. "dev" matches the package
# default and keeps `docker build` without --build-arg working.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/no-inspi/karpenter-scaleway/pkg/version.Version=${VERSION}" \
    -o /out/karpenter-provider-scaleway ./cmd/controller

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/karpenter-provider-scaleway /usr/local/bin/karpenter-provider-scaleway
USER nonroot
ENTRYPOINT ["/usr/local/bin/karpenter-provider-scaleway"]
