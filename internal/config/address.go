package config

import (
	"net"
	"strings"
)

// BindAddr is the address passed to net.Listen. It stays exactly as configured
// so an operator can choose a specific interface.
func (c Config) BindAddr() string { return c.Listen }

// ConnectAddr is the address local tools use to reach the proxy. A wildcard
// bind (0.0.0.0 or ::) listens on every interface but is not itself a valid
// destination: Windows rejects a connect to 0.0.0.0 with WSAEADDRNOTAVAIL.
func (c Config) ConnectAddr() string { return connectAddr(c.Listen) }

// UIConnectAddr is the loopback address of the setup UI. The UI is always bound
// to loopback, so this differs from ConnectAddr only when a caller passed a
// wildcard by mistake.
func (c Config) UIConnectAddr() string { return connectAddr(c.UIListen) }

func connectAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if !isWildcardHost(host) {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// AdvertiseURL is the URL to put in another device's Codex or Claude Code
// config. A specific listen host wins because a VPN, Docker or WSL adapter may
// otherwise be the first address returned by the OS.
func (c Config) AdvertiseURL() string {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return "http://" + connectAddr(c.Listen)
	}
	if host != "" && !isWildcardHost(host) && host != "localhost" {
		return "http://" + net.JoinHostPort(host, port)
	}
	if host == "localhost" {
		return "http://" + net.JoinHostPort("127.0.0.1", port)
	}
	lan := routeLocalIP()
	if lan == "" {
		lan = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(lan, port)
}

// IsLoopbackListen reports whether the proxy is reachable only from this
// machine. It is used by the UI to warn that LAN access is still disabled.
func (c Config) IsLoopbackListen() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	return host == "localhost" || isLoopbackHost(host)
}

func isWildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return isLoopbackHost(host)
}

// SameEndpoint compares two host:port values after normalising loopback
// spellings. A wildcard on either side conflicts with every host on that port,
// because net.Listen cannot bind the same port twice.
func SameEndpoint(left, right string) bool {
	leftHost, leftPort, leftErr := net.SplitHostPort(left)
	rightHost, rightPort, rightErr := net.SplitHostPort(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	if leftPort != rightPort {
		return false
	}
	if isWildcardHost(leftHost) || isWildcardHost(rightHost) {
		return true
	}
	return strings.EqualFold(normaliseHost(leftHost), normaliseHost(rightHost))
}

func normaliseHost(host string) string {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return "127.0.0.1"
	}
	return host
}

// routeLocalIP is a variable so tests can inject the default-route interface
// instead of depending on the network of the machine running the tests.
var routeLocalIP = detectRouteLocalIP

func detectRouteLocalIP() string {
	// Opening a UDP socket sends no packets. It asks the kernel which source
	// address it would use for the default route, which avoids picking a
	// Docker, WSL or VPN adapter that happens to be enumerated first.
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	address, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || address.IP == nil || address.IP.IsLoopback() || address.IP.IsUnspecified() {
		return ""
	}
	return address.IP.String()
}
