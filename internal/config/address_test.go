package config

import "testing"

func TestConnectAddrMapsWildcardToLoopback(t *testing.T) {
	cfg := Default()
	cfg.Listen = "0.0.0.0:8787"
	if cfg.BindAddr() != "0.0.0.0:8787" {
		t.Fatalf("BindAddr=%q", cfg.BindAddr())
	}
	if cfg.ConnectAddr() != "127.0.0.1:8787" {
		t.Fatalf("ConnectAddr=%q", cfg.ConnectAddr())
	}
	cfg.Listen = "192.168.1.5:8787"
	if cfg.ConnectAddr() != "192.168.1.5:8787" {
		t.Fatalf("ConnectAddr=%q", cfg.ConnectAddr())
	}
}

func TestAdvertiseURLPrefersASpecificHost(t *testing.T) {
	original := routeLocalIP
	t.Cleanup(func() { routeLocalIP = original })
	routeLocalIP = func() string { return "192.168.1.20" }

	cfg := Default()
	cfg.Listen = "0.0.0.0:8787"
	if got := cfg.AdvertiseURL(); got != "http://192.168.1.20:8787" {
		t.Fatalf("wildcard advertise=%q", got)
	}
	cfg.Listen = "192.168.1.5:8787"
	if got := cfg.AdvertiseURL(); got != "http://192.168.1.5:8787" {
		t.Fatalf("specific advertise=%q", got)
	}
	routeLocalIP = func() string { return "" }
	cfg.Listen = "0.0.0.0:8787"
	if got := cfg.AdvertiseURL(); got != "http://127.0.0.1:8787" {
		t.Fatalf("fallback advertise=%q", got)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	cfg := Default()
	if !cfg.IsLoopbackListen() {
		t.Fatal("default listen must be loopback")
	}
	cfg.Listen = "0.0.0.0:8787"
	if cfg.IsLoopbackListen() {
		t.Fatal("wildcard listen must not be loopback-only")
	}
}

func TestSameEndpointTreatsWildcardAsConflict(t *testing.T) {
	cases := []struct {
		left, right string
		want        bool
	}{
		{"0.0.0.0:8788", "127.0.0.1:8788", true},
		{"127.0.0.1:8787", "127.0.0.1:8788", false},
		{"localhost:8788", "127.0.0.1:8788", true},
		{"192.168.1.2:8788", "127.0.0.1:8788", false},
		{"[::]:8788", "127.0.0.1:8788", true},
	}
	for _, item := range cases {
		if got := SameEndpoint(item.left, item.right); got != item.want {
			t.Fatalf("SameEndpoint(%q,%q)=%v want %v", item.left, item.right, got, item.want)
		}
	}
}
