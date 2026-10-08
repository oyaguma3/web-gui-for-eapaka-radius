// eapaka-webgui は EAP-AKA RADIUS PoC（eapaka-radius-server-poc）の管理 GUI（BFF + Web GUI）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	// distroless のイメージにはタイムゾーンのデータがないので、埋め込んで環境変数 TZ を効かせる。
	_ "time/tzdata"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/certs"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/config"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/server"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/web"
)

// version はビルド時に -ldflags "-X main.version=..." で埋め込む。
var version = "dev"

const usage = `usage: eapaka-webgui <command>

commands:
  serve              サーバーを起動する（コマンド省略時の既定）
  check-admin        本PoCの Provisioning API に接続できるか確かめる
  gen-client-cert    Provisioning API に提示するクライアント証明書と秘密鍵を作る
                     （eapaka-webgui gen-client-cert -h で使い方を表示）

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
	case "check-admin":
		err = checkAdmin(ctx)
	case "gen-client-cert":
		err = genClientCert(args[1:], os.Stdout, os.Stderr)
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
	if err := cfg.CheckServe(); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	log.Info("starting", "version", version)

	openCtx, cancelOpen := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.Open(openCtx, store.Options{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	cancelOpen()
	if err != nil {
		return err
	}
	defer st.Close()
	authSvc, err := auth.New(ctx, auth.Options{
		Store:                st,
		Log:                  log,
		InitialAdminID:       cfg.InitialAdminID,
		InitialAdminPassword: cfg.InitialAdminPassword,
		SessionIdleTimeout:   cfg.SessionIdleTimeout,
		SessionMaxAge:        cfg.SessionMaxAge,
		MaxLoginFailures:     cfg.LoginMaxFailures,
		LockDuration:         cfg.LoginLockDuration,
		AuditMaxLen:          cfg.AuditMaxLen,
	})
	if err != nil {
		return err
	}
	log.Info("initial admin", "user_id", cfg.InitialAdminID)

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

	prov, err := newProvClient(cfg, log)
	if err != nil {
		return err
	}
	log.Info("provisioning api client", "url", prov.BaseURL(),
		"client_cert_fingerprint", certs.Fingerprint(prov.ClientCertificate()),
		"client_cert_not_after", prov.ClientCertificate().NotAfter)
	// 起動時点で provisioning-api が動いていなくても起動は続ける。画面で接続できないことを示す。
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if st, err := prov.Status(checkCtx); err != nil {
		log.Warn("provisioning api is not available", "error", err, "hint", provapi.Diagnose(err))
	} else {
		log.Info("provisioning api is available", "server_version", st.Version, "node_name", st.NodeName)
	}
	cancel()

	h, err := web.New(web.Options{Log: log, Version: version, Prov: prov, Auth: authSvc})
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

func newProvClient(cfg config.Config, log *slog.Logger) (*provapi.Client, error) {
	return provapi.New(provapi.Options{
		BaseURL:        cfg.AdminURL,
		ClientCertFile: cfg.AdminClientCertFile,
		ClientKeyFile:  cfg.AdminClientKeyFile,
		ServerCertFile: cfg.AdminServerCertFile,
		Log:            log,
	})
}

// checkAdmin は Provisioning API への接続を確かめ、結果を表示する。導入時の確認に使う。
func checkAdmin(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	prov, err := newProvClient(cfg, log)
	if err != nil {
		return err
	}
	fmt.Println("接続先:", prov.BaseURL())
	fmt.Println("クライアント証明書のフィンガープリント:", certs.Fingerprint(prov.ClientCertificate()))

	st, err := prov.Status(ctx)
	if err != nil {
		if hint := provapi.Diagnose(err); hint != "" {
			fmt.Println(hint)
		}
		return err
	}
	fmt.Printf("接続できました。provisioning-api %s（ノード %s、加入者 %d、RADIUSクライアント %d、認可ポリシー %d）\n",
		st.Version, st.NodeName, st.SubscriberCount, st.ClientCount, st.PolicyCount)
	return nil
}
