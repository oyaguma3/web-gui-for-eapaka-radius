package config

import (
	"log/slog"
	"slices"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	for _, name := range []string{"EAPAKA_WEBGUI_ADDR", "EAPAKA_WEBGUI_TLS_CERT", "EAPAKA_WEBGUI_TLS_KEY", "EAPAKA_WEBGUI_TLS_HOSTS", "EAPAKA_WEBGUI_LOG_LEVEL"} {
		t.Setenv(name, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8445" || c.TLSCertFile != "/data/tls/cert.pem" || c.TLSKeyFile != "/data/tls/key.pem" {
		t.Errorf("got %+v", c)
	}
	if !slices.Equal(c.TLSHosts, []string{"localhost", "127.0.0.1"}) {
		t.Errorf("TLSHosts = %v", c.TLSHosts)
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}
}

func TestLoad(t *testing.T) {
	t.Setenv("EAPAKA_WEBGUI_ADDR", "100.64.0.1:443")
	t.Setenv("EAPAKA_WEBGUI_TLS_HOSTS", " gui.example.ts.net , 100.64.0.1,, ")
	t.Setenv("EAPAKA_WEBGUI_LOG_LEVEL", "debug")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "100.64.0.1:443" {
		t.Errorf("Addr = %q", c.Addr)
	}
	if !slices.Equal(c.TLSHosts, []string{"gui.example.ts.net", "100.64.0.1"}) {
		t.Errorf("TLSHosts = %v", c.TLSHosts)
	}
	if c.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}

	t.Setenv("EAPAKA_WEBGUI_LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Error("bad log level: want error")
	}
}
