// eapaka-webgui は EAP-AKA RADIUS PoC（eapaka-radius-server-poc）の管理 GUI（BFF + Web GUI）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	// distroless のイメージにはタイムゾーンのデータがないので、埋め込んで環境変数 TZ を効かせる。
	_ "time/tzdata"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/certs"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/config"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/server"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/web"
)

// version はビルド時に -ldflags "-X main.version=..." で埋め込む。
var version = "dev"

const usage = `usage: eapaka-webgui <command>

commands:
  serve    サーバーを起動する（コマンド省略時の既定）

設定は環境変数で与える。
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve"}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch args[0] {
	case "serve":
		err = serve(ctx)
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	log.Info("starting", "version", version)

	created, err := certs.EnsureFiles(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSHosts)
	if err != nil {
		return err
	}
	if created {
		log.Info("generated self-signed server certificate", "cert", cfg.TLSCertFile, "hosts", cfg.TLSHosts)
	}
	cert, err := certs.LoadFile(cfg.TLSCertFile, cfg.TLSKeyFile, log)
	if err != nil {
		return err
	}
	log.Info("loaded server certificate", "cert", cfg.TLSCertFile,
		"fingerprint", certs.Fingerprint(cert.Leaf()), "not_after", cert.Leaf().NotAfter)

	h, err := web.New(web.Options{Log: log, Version: version})
	if err != nil {
		return err
	}
	return server.Run(ctx, server.Options{
		Addr:           cfg.Addr,
		GetCertificate: cert.GetCertificate,
		Handler:        h.Routes(),
		Log:            log,
	})
}
