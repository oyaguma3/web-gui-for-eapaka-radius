FROM golang:1.27.1 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /eapaka-webgui ./cmd/eapaka-webgui
# ブラウザ向けの自己署名証明書を保存するディレクトリ。ボリュームの初期の所有者を nonroot にするため、ここで作る。
RUN mkdir -p /out/data/tls

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /eapaka-webgui /eapaka-webgui
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENTRYPOINT ["/eapaka-webgui"]
CMD ["serve"]
