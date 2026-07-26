# Build in the module cache, so a source-only change does not re-download the
# k8s dependency tree.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# TARGETOS/TARGETARCH come from buildx, so one native build stage cross-compiles
# for every platform rather than emulating each under QEMU.
ARG TARGETOS
ARG TARGETARCH
# CGO off keeps the binary static, which is what lets the runtime stage be
# scratch-like with no libc.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/tunnel .

FROM gcr.io/distroless/static:nonroot

# 65532 is distroless' nonroot user, matching runAsUser in the install manifest.
USER 65532:65532

COPY --from=build /out/tunnel /tunnel

ENTRYPOINT ["/tunnel"]
