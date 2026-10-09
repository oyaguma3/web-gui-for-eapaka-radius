// eapaka-webgui は EAP-AKA RADIUS PoC（eapaka-radius-server-poc）の管理 GUI（BFF + Web GUI）。
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	// distroless のイメージにはタイムゾーンのデータがないので、埋め込んで環境変数 TZ を効かせる。
	_ "time/tzdata"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/certs"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/config"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/server"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/web"
)

// version はビルド時に -ldflags "-X main.version=..." で埋め込む。
var version = "dev"

const usage = `usage: eapaka-webgui <command>

commands:
  serve              サーバーを起動する（コマンド省略時の既定）
  check-admin        接続先（本PoCの Provisioning API、または eapaka-node-provisioner）に接続できるか確かめる
  gen-client-cert    接続先に提示するクライアント証明書と秘密鍵を作る
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
	log.Info("provisioning api client", "admin_api", string(cfg.AdminAPI), "url", prov.BaseURL(),
		"client_cert_fingerprint", certs.Fingerprint(prov.ClientCertificate()),
		"client_cert_not_after", prov.ClientCertificate().NotAfter, "timeout", cfg.AdminTimeout.String())
	// 起動時点で接続先が動いていなくても起動は続ける。画面で接続できないことを示す。
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	checkAtStartup(checkCtx, log, cfg, prov)
	cancel()

	opts := web.Options{Log: log, Version: version, Prov: prov, Auth: authSvc}
	if cfg.AdminAPI == config.AdminAPIProvisioner {
		// provisioner 経由では、加入者・状態・監査ログ・操作の記録の画面が provisioner だけの API を使う。
		opts.PV = pvapi.New(prov)
	}
	h, err := web.New(opts)
	if err != nil {
		return err
	}
	return server.Run(ctx, server.Options{
		Addr:           cfg.Addr,
		GetCertificate: cert.GetCertificate,
		Handler:        h.Routes(),
		Log:            log,
		// 画面の 1 回の要求で接続先を何度か呼ぶことがあるので、呼び出しの上限より長くする
		// （provisioner 経由では 1 回の呼び出しの上限が 60 秒）。
		WriteTimeout: max(30*time.Second, cfg.AdminTimeout+30*time.Second),
	})
}

// newProvClient は接続先のクライアントを作る。provisioner 経由でも、中継の部分（RADIUSクライアント・認可ポリシー・
// セッション・鍵の取得）はこのクライアントをそのまま使う（provisioner が Provisioning API と同じ形で中継する）。
func newProvClient(cfg config.Config, log *slog.Logger) (*provapi.Client, error) {
	opts := provapi.Options{
		BaseURL:        cfg.AdminURL,
		ClientCertFile: cfg.AdminClientCertFile,
		ClientKeyFile:  cfg.AdminClientKeyFile,
		ServerCertFile: cfg.AdminServerCertFile,
		Timeout:        cfg.AdminTimeout,
		Log:            log,
	}
	if cfg.AdminAPI == config.AdminAPIProvisioner {
		opts.Name = pvapi.Name
	}
	return provapi.New(opts)
}

// notProvisioner は、provisioner の設定で provisioner でない相手につながったときの文。
const notProvisioner = "接続先は provisioner ではないようです（provisioning-api など）。EAPAKA_WEBGUI_ADMIN_API（provisioner）と EAPAKA_WEBGUI_ADMIN_URL を確認してください。"

// mismatch は、設定した接続先の種類と、実際の接続先（/status の形から見分けたもの）が違えば、その旨の文を返す。
func mismatch(want config.AdminAPI, got pvapi.API) string {
	if got == "" || string(got) == string(want) {
		return ""
	}
	return fmt.Sprintf("接続先は %s のようです。EAPAKA_WEBGUI_ADMIN_API（%s）と EAPAKA_WEBGUI_ADMIN_URL を確認してください。", got, want)
}

// checkAtStartup は、起動時に接続先を確かめてログに出す。
// provisioner の /status は下流 2 つを確かめるので時間がかかることがある（下流が止まっていると下流ごとの上限まで）。
// provisioner 経由のときは /status を 1 回だけ呼び、応答の形で接続先の種類も確かめる。
func checkAtStartup(ctx context.Context, log *slog.Logger, cfg config.Config, prov *provapi.Client) {
	if cfg.AdminAPI == config.AdminAPIProvisioner {
		st, err := pvapi.New(prov).Status(ctx)
		if err != nil {
			log.Warn("provisioner is not available", "error", err, "hint", pvapi.Diagnose(err))
			return
		}
		if !st.IsProvisioner() {
			log.Warn("unexpected admin api", "admin_api", string(cfg.AdminAPI), "hint", notProvisioner)
			return
		}
		log.Info("provisioner is available", "server_version", st.Version,
			"prov_reachable", st.Downstreams.Prov.Reachable, "aka_configured", st.Downstreams.Aka.Configured,
			"aka_reachable", st.Downstreams.Aka.Reachable)
		for name, d := range map[string]pvapi.DownstreamStatus{"prov": st.Downstreams.Prov, "aka": st.Downstreams.Aka} {
			if d.Configured && !d.Reachable {
				log.Warn("provisioner cannot reach its downstream", "downstream", name, "error", d.Error, "hint", d.Hint)
			}
		}
		return
	}
	got, err := pvapi.Probe(ctx, prov)
	if err != nil {
		log.Warn("provisioning api is not available", "error", err, "hint", provapi.Diagnose(err))
		return
	}
	if msg := mismatch(cfg.AdminAPI, got); msg != "" {
		log.Warn("unexpected admin api", "admin_api", string(cfg.AdminAPI), "detected", string(got), "hint", msg)
		return
	}
	st, err := prov.Status(ctx)
	if err != nil {
		log.Warn("provisioning api is not available", "error", err, "hint", provapi.Diagnose(err))
		return
	}
	log.Info("provisioning api is available", "server_version", st.Version, "node_name", st.NodeName)
}

// checkAdmin は接続先への接続を確かめ、結果を表示する。導入時の確認に使う。
// 接続先に接続できない、または設定と違う種類の接続先だった場合はエラーを返す。
// provisioner の先の下流に接続できない場合は、表示はするがエラーにはしない（provisioner の check-downstream で確かめる）。
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
	fmt.Printf("接続先: %s（%s）\n", prov.BaseURL(), cfg.AdminAPI)
	fmt.Println("クライアント証明書のフィンガープリント:", certs.Fingerprint(prov.ClientCertificate()))

	if cfg.AdminAPI == config.AdminAPIProvisioner {
		return printProvisionerStatus(ctx, pvapi.New(prov))
	}
	got, err := pvapi.Probe(ctx, prov)
	if err != nil {
		if hint := provapi.Diagnose(err); hint != "" {
			fmt.Println(hint)
		}
		return err
	}
	if msg := mismatch(cfg.AdminAPI, got); msg != "" {
		fmt.Println(msg)
		return fmt.Errorf("unexpected admin api: %s", got)
	}
	st, err := prov.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("接続できました。provisioning-api %s（ノード %s、加入者 %d、RADIUSクライアント %d、認可ポリシー %d）\n",
		st.Version, st.NodeName, st.SubscriberCount, st.ClientCount, st.PolicyCount)
	return nil
}

// printProvisionerStatus は provisioner の状態（下流 2 つへの接続、AVクライアント、PLMN マップ）を表示する。
func printProvisionerStatus(ctx context.Context, pv *pvapi.Client) error {
	st, err := pv.Status(ctx)
	if err != nil {
		if hint := pvapi.Diagnose(err); hint != "" {
			fmt.Println(hint)
		}
		return err
	}
	if !st.IsProvisioner() {
		fmt.Println(notProvisioner)
		return errors.New("unexpected admin api: not provisioner")
	}
	fmt.Printf("接続できました。provisioner %s\n", st.Version)
	printDownstream("本PoCの Provisioning API", st.Downstreams.Prov)
	if !st.Downstreams.Aka.Configured {
		fmt.Println("  aka-only-server: 設定なし（すべての加入者の鍵を本PoCに置く）")
	} else {
		printDownstream("aka-only-server", st.Downstreams.Aka)
		// aka-only-server に接続できなければ、AVクライアントは確かめられていない（exists が false になる）ので出さない。
		switch av := st.AVClient; {
		case !st.Downstreams.Aka.Reachable:
		case !av.Exists:
			fmt.Printf("  vector-gateway の AVクライアント（ID %d）が aka-only-server にありません。\n", av.ID)
		case !av.Enabled:
			fmt.Printf("  vector-gateway の AVクライアント: ID %d（%s）は無効になっています。\n", av.ID, av.Name)
		default:
			fmt.Printf("  vector-gateway の AVクライアント: ID %d（%s）\n", av.ID, av.Name)
		}
	}
	var entries []string
	for _, e := range st.PLMNMap {
		entries = append(entries, e.PLMN+"="+string(e.KeyStore))
	}
	fmt.Println("  PLMN マップ:", cmp.Or(strings.Join(entries, ","), "（なし。すべての加入者の鍵を本PoCに置く）"))
	if ops := st.Operations; ops != nil && ops.Running+ops.Retrying+ops.Failed > 0 {
		fmt.Printf("  未完了の操作: 実行中 %d、やり直し中 %d、失敗 %d\n", ops.Running, ops.Retrying, ops.Failed)
	}
	return nil
}

func printDownstream(label string, d pvapi.DownstreamStatus) {
	if !d.Reachable {
		fmt.Printf("  %s: provisioner から接続できません（%s）\n", label, d.Error)
		if d.Hint != "" {
			fmt.Println("    " + d.Hint)
		}
		return
	}
	if d.NodeName != "" {
		fmt.Printf("  %s: %s（ノード %s、加入者 %d）\n", label, d.Version, d.NodeName, d.SubscriberCount)
	} else {
		fmt.Printf("  %s: %s（加入者 %d）\n", label, d.Version, d.SubscriberCount)
	}
}
