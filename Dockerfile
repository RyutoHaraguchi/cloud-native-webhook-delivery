# cmd/ 配下のコマンドを 1 つビルドする。例: docker build --build-arg SERVICE=dummy-receiver .
FROM golang:1.27.3-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN test -n "$SERVICE" && CGO_ENABLED=0 go build -trimpath -o /out/app "./cmd/$SERVICE"

# CA 証明書を入れないので、TLS で接続する処理（https の送信先、AWS の API）はこのイメージでは動かない。
# ローカルの compose は http だけで動く。AWS 用のイメージはフェーズ 5 で CA 証明書を入れて見直す
FROM scratch
COPY --from=build /out/app /app
USER 65532:65532
ENTRYPOINT ["/app"]
