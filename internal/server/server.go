// Package server はブラウザ向けの HTTPS リスナーを起動する。
package server

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	shutdownTimeout = 10 * time.Second
	// defaultWriteTimeout は、要求を受け取ってから応答を書き終えるまでの上限の既定。
	defaultWriteTimeout = 30 * time.Second
)

// Options はリスナーの設定。
type Options struct {
	// Addr は待ち受けアドレス。
	Addr string
	// GetCertificate はサーバー証明書を返す。
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Handler        http.Handler
	Log            *slog.Logger
	// WriteTimeout は、要求を受け取ってから応答を書き終えるまでの上限（ハンドラーの処理の時間を含む）。0 なら 30 秒。
	// 接続先の 1 回の呼び出しの上限より長くする（短いと、時間のかかった応答を書き出せずに接続が切れる）。
	WriteTimeout time.Duration
}

// Run は HTTPS リスナーを起動し、ctx が終了するかリスナーが失敗するまで待つ。
func Run(ctx context.Context, opts Options) error {
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.Addr, err)
	}
	return serve(ctx, ln, opts)
}

func serve(ctx context.Context, ln net.Listener, opts Options) error {
	// HSTS は付けない。自己署名の証明書でブラウザの警告を通して使う運用では、
	// HSTS が付くとその警告を越えられなくなるため。
	srv := &http.Server{
		Handler: opts.Handler,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: opts.GetCertificate,
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      cmp.Or(opts.WriteTimeout, defaultWriteTimeout),
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(opts.Log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		// 証明書は TLSConfig.GetCertificate から取るので、ファイル名は渡さない。
		errc <- srv.ServeTLS(ln, "", "")
	}()
	opts.Log.Info("listening", "addr", ln.Addr().String())

	select {
	case <-ctx.Done():
		opts.Log.Info("shutting down")
	case err := <-errc:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		opts.Log.Warn("shutdown", "error", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
