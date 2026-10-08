package config

import (
	"log/slog"
	"slices"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	for _, name := range []string{"EAPAKA_WEBGUI_ADDR", "EAPAKA_WEBGUI_TLS_CERT", "EAPAKA_WEBGUI_TLS_KEY", "EAPAKA_WEBGUI_TLS_HOSTS", "EAPAKA_WEBGUI_LOG_LEVEL",
		"EAPAKA_WEBGUI_ADMIN_URL", "EAPAKA_WEBGUI_ADMIN_CLIENT_CERT", "EAPAKA_WEBGUI_ADMIN_CLIENT_KEY", "EAPAKA_WEBGUI_ADMIN_SERVER_CERT"} {
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
	if c.AdminURL != "https://provisioning-api:9444/admin/v1" || c.AdminClientCertFile != "/certs/admin-client.pem" ||
		c.AdminClientKeyFile != "" || c.AdminServerCertFile != "/certs/admin-server.pem" {
		t.Errorf("admin = %q %q %q %q", c.AdminURL, c.AdminClientCertFile, c.AdminClientKeyFile, c.AdminServerCertFile)
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
