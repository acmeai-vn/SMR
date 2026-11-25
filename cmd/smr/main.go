// Command smr is the local Smart Model Router daemon and lifecycle CLI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/acmeai-vn/SMR/internal/classifier"
	"github.com/acmeai-vn/SMR/internal/config"
	"github.com/acmeai-vn/SMR/internal/fileperm"
	"github.com/acmeai-vn/SMR/internal/hostcfg"
	"github.com/acmeai-vn/SMR/internal/logtail"
	"github.com/acmeai-vn/SMR/internal/proxy"
	"github.com/acmeai-vn/SMR/internal/service"
	"github.com/acmeai-vn/SMR/internal/smrpaths"
	"github.com/acmeai-vn/SMR/internal/ui"
	"github.com/acmeai-vn/SMR/internal/update"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(ctx, os.Args[2:])
	case "setup":
		err = runSetup(ctx)
	case "status":
		err = runStatus(ctx)
	case "doctor":
		err = runDoctor(ctx)
	case "logs":
		err = runLogs(os.Args[2:])
	case "restart":
		err = restartDaemon(ctx)
	case "update":
		err = runUpdate(ctx, os.Args[2:])
	case "uninstall":
		err = runUninstall(ctx, os.Args[2:])
	case "migrate-config":
		err = runMigrate()
	case "keys":
		err = runKeys(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("SMR", version)
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("unknown command %q; run `smr help`", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "smr:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`SMR - Smart Model Router

Usage:
  smr serve [--background]
  smr setup
  smr status
  smr doctor
  smr logs [--tail N]
  smr restart
  smr update [--check] [--version vX.Y.Z] [--yes]
  smr uninstall [--purge]
  smr migrate-config
  smr keys list
  smr keys add <name>
  smr keys revoke <name>
  smr version`)
}

func runServe(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	background := flags.Bool("background", false, "hide the console window; used by autostart")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *background {
		hideConsoleWindow()
	}
	configPath, dataDir, err := paths()
	if err != nil {
		return err
	}
	manager, err := config.LoadManager(configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := manager.Update(manager.Snapshot()); err != nil {
			return fmt.Errorf("create default config: %w", err)
		}
	}
	cfg := manager.Snapshot()
	if err := cfg.ValidateListenerPlacement(); err != nil {
		return err
	}
	// Take the single-instance lock before touching the token or the ports. A
	// second start - an update, or `smr setup` racing a slow boot - must not
	// replace the token the running daemon still expects.
	lock, err := service.AcquireLock(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	// The first start on an upgraded install has no access key. Create the key
	// for this machine before validating: otherwise Codex and Claude Code would
	// receive 401 and the only repair command would itself be blocked by the
	// runtime check. This is the one place SMR edits config without a user
	// action, and it only appends a key.
	createdKey := ""
	if len(cfg.AccessKeys) == 0 {
		key, err := config.GenerateAccessKey()
		if err != nil {
			return err
		}
		key.Name = "local"
		if err := cfg.AddAccessKey(key); err != nil {
			return err
		}
		if err := manager.Update(cfg); err != nil {
			return fmt.Errorf("save the first access key: %w", err)
		}
		cfg = manager.Snapshot()
		createdKey = key.Key
	}
	if err := cfg.ValidateRuntime(); err != nil {
		return err
	}
	proxyListener, err := net.Listen("tcp", cfg.BindAddr())
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.BindAddr(), err)
	}
	defer proxyListener.Close()
	uiListener, err := net.Listen("tcp", cfg.UIListen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.UIListen, err)
	}
	defer uiListener.Close()

	// Both ports belong to this process now, so a token written here always
	// describes the daemon that serves the setup page.
	token, err := ui.GenerateToken()
	if err != nil {
		return err
	}
	if _, err := ui.WriteTokenFile(dataDir, token); err != nil {
		return err
	}
	logFile, err := openLog(dataDir)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stdout, logFile), &slog.HandlerOptions{Level: slog.LevelInfo}))
	if createdKey != "" {
		logger.Info("created the local SMR access key; add it to Codex or Claude Code", "name", "local", "key", createdKey)
	}

	router := proxy.New(manager, logger)
	setupUI, err := ui.New(manager, router.Snapshot, dataDir, token, version, logger)
	if err != nil {
		return err
	}
	proxyServer := &http.Server{
		Handler:           router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	uiServer := &http.Server{
		Handler:           setupUI.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() {
		logger.Info("SMR listening", "addr", cfg.BindAddr(), "version", version)
		errCh <- proxyServer.Serve(proxyListener)
	}()
	go func() {
		logger.Info("setup UI listening", "addr", cfg.UIListen)
		errCh <- uiServer.Serve(uiListener)
	}()
	fmt.Printf("SMR setup: http://%s/?token=%s\n", cfg.UIConnectAddr(), token)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = proxyServer.Shutdown(shutdownCtx)
	_ = uiServer.Shutdown(shutdownCtx)
	return nil
}

func runSetup(ctx context.Context) error {
	_, dataDir, err := paths()
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, err := service.HealthCheck(ctx, "http://"+cfg.ConnectAddr()); err != nil {
		// The daemon may have been stopped by a reboot, a crash or an update.
		// Restart through the service manager instead of starting a detached
		// process: a detached daemon holds the pid lock, so a later
		// `systemctl restart` would spawn a process that exits immediately
		// while the manager still reports success. Restart already falls back
		// to a detached start when no unit is installed.
		fmt.Println("SMR is not running; starting it...")
		if err := restartDaemon(ctx); err != nil {
			return err
		}
	}
	token, err := ui.ReadTokenFile(dataDir)
	if err != nil {
		return fmt.Errorf("setup token not found; restart SMR, then run `smr setup` again")
	}
	target := fmt.Sprintf("http://%s/?token=%s", cfg.UIConnectAddr(), token)
	fmt.Println("Opening SMR setup:", target)
	if err := service.OpenBrowser(ctx, target); err != nil {
		fmt.Println("Open this URL manually:", target)
	}
	return nil
}

// restartDaemon restarts the service manager entry (or a detached daemon) and
// waits for /health. systemctl, launchctl and schtasks report success as soon
// as the process is spawned, so without this check a daemon that exits on a
// busy port or a held lock looks like a successful restart.
func restartDaemon(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := service.Restart(ctx); err != nil {
		return err
	}
	if err := waitForHealth(ctx, "http://"+cfg.ConnectAddr(), 15*time.Second); err != nil {
		return fmt.Errorf("SMR restarted but is not healthy at %s: %w", cfg.ConnectAddr(), err)
	}
	return nil
}

// waitForHealth polls the daemon until it answers or the deadline passes. The
// daemon needs a moment to bind both ports after it is started.
func waitForHealth(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := service.HealthCheck(ctx, baseURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return lastErr
}

func runStatus(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	health, err := service.HealthCheck(ctx, "http://"+cfg.ConnectAddr())
	if err != nil {
		return fmt.Errorf("SMR is not reachable at %s: %w", cfg.ConnectAddr(), err)
	}
	fmt.Printf("daemon: %s (version %s)\n", health.Status, health.Version)
	keyState := "missing"
	if cfg.Classifier.APIKey != "" {
		keyState = "configured"
	}
	fmt.Printf("classifier: %s (key %s)\n", cfg.Classifier.BaseURL, keyState)
	fmt.Printf("advertise: %s\n", cfg.AdvertiseURL())
	fmt.Printf("access keys: %d\n", len(cfg.AccessKeys))
	return nil
}

func runDoctor(ctx context.Context) error {
	configPath, dataDir, err := paths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Println("config: FAIL", err)
		return err
	}
	fmt.Println("config: OK", configPath)
	fmt.Println("data:  ", dataDir)
	fmt.Println("listen:", cfg.Listen, " ui:", cfg.UIListen)
	if err := cfg.ValidateRuntime(); err != nil {
		fmt.Println("runtime: FAIL", err)
	} else {
		fmt.Println("runtime: OK")
	}
	for _, key := range cfg.AccessKeys {
		fmt.Printf("access key: %s (created %s)\n", key.Name, key.CreatedAt)
	}
	if !cfg.IsLoopbackListen() {
		fmt.Println("warning: the proxy is reachable over the LAN over plain HTTP; use a private network such as Tailscale or WireGuard")
	}
	if cfg.Classifier.APIKey == "" {
		fmt.Println("classifier key: MISSING (run `smr setup`)")
	} else {
		fmt.Println("classifier key: configured")
		client := classifier.New(cfg.Classifier.BaseURL, cfg.Classifier.APIKey, 10*time.Second, 0)
		account, err := client.Account(ctx)
		if err != nil {
			fmt.Println("classifier connection: FAIL", err)
		} else {
			fmt.Printf("classifier connection: OK (balance %d %s, cache %ds)\n", account.BalanceVND, account.Currency, account.CacheTTLSeconds)
		}
	}
	if _, err := service.HealthCheck(ctx, "http://"+cfg.ConnectAddr()); err != nil {
		fmt.Println("daemon: FAIL", err)
	} else {
		fmt.Println("daemon: OK")
	}
	for _, detection := range hostcfg.Detect() {
		state := "not found"
		if detection.Exists {
			state = "found"
		}
		fmt.Printf("host %-6s %-9s %s\n", detection.Host, state, detection.Path)
	}
	for _, tier := range config.Tiers {
		for _, profile := range []string{"codex", "claude"} {
			if cfg.Profiles[profile].Tiers[tier].Model == "" {
				fmt.Printf("warning: profiles.%s.tiers.%s is not configured\n", profile, tier)
			}
		}
	}
	return nil
}

func runLogs(args []string) error {
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	tail := flags.Int("tail", 100, "number of lines")
	if err := flags.Parse(args); err != nil {
		return err
	}
	_, dataDir, err := paths()
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, ui.LogFile)
	body, err := logtail.Tail(path, *tail)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	fmt.Print(body)
	return nil
}

func runUpdate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	check := flags.Bool("check", false, "check without installing")
	requested := flags.String("version", "", "install a specific release")
	yes := flags.Bool("yes", false, "do not ask for confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	manifest, err := update.Fetch(ctx, *requested)
	if err != nil {
		return err
	}
	fmt.Printf("current: %s\nlatest:  %s\n", version, manifest.Version)
	if *check {
		return nil
	}
	if manifest.Version == version {
		fmt.Println("already up to date")
		return nil
	}
	if !*yes {
		fmt.Print("Install this release? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") {
			fmt.Println("cancelled")
			return nil
		}
	}
	if err := update.Install(ctx, manifest); err != nil {
		return err
	}
	fmt.Println("installed", manifest.Version)
	if err := restartDaemon(ctx); err != nil {
		return fmt.Errorf("installed %s but the daemon did not come back: %w", manifest.Version, err)
	}
	fmt.Println("SMR is healthy")
	return nil
}

func runUninstall(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := flags.Bool("purge", false, "remove SMR config and local state")
	if err := flags.Parse(args); err != nil {
		return err
	}
	configPath, dataDir, err := paths()
	if err != nil {
		return err
	}
	if err := service.Uninstall(ctx); err != nil {
		fmt.Println("service removal:", err)
	}
	if *purge {
		if err := safeRemove(configPath); err != nil {
			return err
		}
		if err := safeRemoveAll(dataDir); err != nil {
			return err
		}
		fmt.Println("removed config and local state")
	}
	fmt.Println("SMR uninstalled")
	return nil
}

func runMigrate() error {
	configPath, dataDir, err := paths()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("no config to migrate")
		return nil
	}
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return err
	}
	if _, ok := raw["profiles"]; ok {
		if _, hasOld := raw["tiers"]; !hasOld {
			fmt.Println("config already uses profiles; nothing to migrate")
			return nil
		}
	}
	backupDir := filepath.Join(dataDir, "backups", "config")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	backup := filepath.Join(backupDir, time.Now().UTC().Format("20060102T150405")+"-config.yaml")
	if err := os.WriteFile(backup, body, 0o600); err != nil {
		return err
	}
	profiles := map[string]any{}
	if existing, ok := raw["profiles"].(map[string]any); ok {
		profiles = existing
	}
	if oldTiers, ok := raw["tiers"].(map[string]any); ok {
		profile, _ := profiles["codex"].(map[string]any)
		if profile == nil {
			profile = map[string]any{"api": "responses", "auth": "bearer"}
		}
		profile["tiers"] = oldTiers
		profiles["codex"] = profile
	}
	raw["profiles"] = profiles
	delete(raw, "tiers")
	delete(raw, "jev")
	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, out, 0o600); err != nil {
		return err
	}
	if _, err := config.Load(configPath); err != nil {
		_ = os.WriteFile(configPath, body, 0o600)
		return fmt.Errorf("migrated config is invalid; original restored: %w", err)
	}
	fmt.Println("migrated", configPath)
	fmt.Println("backup:", backup)
	fmt.Println("warning: removed the old top-level jev block; SMR uses ACME AI's classifier only")
	return nil
}

func loadConfig() (config.Config, error) {
	path, _, err := paths()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
}

func paths() (string, string, error) {
	configPath, err := smrpaths.ConfigPath()
	if err != nil {
		return "", "", err
	}
	dataDir, err := smrpaths.DataDir()
	if err != nil {
		return "", "", err
	}
	// A custom SMR_CONFIG may point into a shared directory. Create the parent
	// but do not replace its ACL; the config file itself is restricted.
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return "", "", err
	}
	if err := smrpaths.EnsureDir(dataDir); err != nil {
		return "", "", err
	}
	return configPath, dataDir, nil
}

func openLog(dataDir string) (*os.File, error) {
	path := filepath.Join(dataDir, ui.LogFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := fileperm.Restrict(path); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func safeRemove(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func safeRemoveAll(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	if abs == string(filepath.Separator) || abs == filepath.VolumeName(abs)+string(filepath.Separator) || (home != "" && abs == home) {
		return fmt.Errorf("refusing to remove unsafe path %s", abs)
	}
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	return nil
}

func runKeys(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: smr keys list | add <name> | revoke <name>")
	}
	switch args[0] {
	case "list":
		return keysList(ctx)
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: smr keys add <name>")
		}
		return keysAdd(ctx, strings.Join(args[1:], " "))
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("usage: smr keys revoke <name>")
		}
		return keysRevoke(ctx, strings.Join(args[1:], " "))
	default:
		return fmt.Errorf("unknown keys command %q; use list, add or revoke", args[0])
	}
}

func keysList(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	usage := map[string]proxy.KeyStats{}
	if stats, err := fetchKeyStats(ctx, cfg); err == nil {
		for _, item := range stats {
			usage[item.Name] = item
		}
	}
	fmt.Printf("advertise: %s\n", cfg.AdvertiseURL())
	fmt.Printf("%-24s %-22s %8s  %s\n", "NAME", "CREATED", "REQUESTS", "LAST SEEN")
	for _, key := range cfg.AccessKeys {
		item := usage[key.Name]
		last := item.LastSeenAt
		if last == "" {
			last = "-"
		}
		fmt.Printf("%-24s %-22s %8d  %s\n", key.Name, key.CreatedAt, item.Requests, last)
	}
	if len(cfg.AccessKeys) == 0 {
		fmt.Println("(no access keys; run `smr keys add <name>`)")
	}
	if cfg.IsLoopbackListen() {
		fmt.Println("LAN access is off: listen is loopback. Set `listen: 0.0.0.0:8787` and restart SMR to let other devices connect.")
	} else {
		fmt.Println("warning: keys and prompts travel over the LAN without TLS; use a trusted network or a VPN.")
	}
	return nil
}

func keysAdd(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if err := config.ValidateAccessKeyName(name); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	manager, configPath, dataDir, err := loadOfflineManager()
	if err != nil {
		return err
	}
	if daemonAlive(ctx, cfg) {
		if !uiAlive(ctx, cfg) {
			return fmt.Errorf("SMR is running but its setup UI is unreachable; run `smr restart`, then retry")
		}
		result, err := uiAccessRequest(ctx, cfg, dataDir, "/access/add", url.Values{"name": {name}})
		if err != nil {
			return fmt.Errorf("add the key through the running daemon: %w", err)
		}
		key, _ := result["key"].(string)
		address, _ := result["advertise_url"].(string)
		fmt.Printf("Access key %q created: %s\n", name, key)
		return printKeyInstructions(cfg, config.AccessKey{Name: name, Key: key}, address)
	}

	key, err := config.GenerateAccessKey()
	if err != nil {
		return err
	}
	key.Name = name
	live := manager.Snapshot()
	if err := live.AddAccessKey(key); err != nil {
		return err
	}
	if err := manager.Update(live); err != nil {
		return fmt.Errorf("save %s: %w", configPath, err)
	}
	fmt.Printf("Access key %q created: %s\n", name, key.Key)
	fmt.Println("SMR is not running; the key takes effect the next time it starts.")
	return printKeyInstructions(live, key, live.AdvertiseURL())
}

func keysRevoke(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	manager, _, dataDir, err := loadOfflineManager()
	if err != nil {
		return err
	}
	if daemonAlive(ctx, cfg) {
		if !uiAlive(ctx, cfg) {
			return fmt.Errorf("SMR is running but its setup UI is unreachable; run `smr restart`, then retry")
		}
		if _, err := uiAccessRequest(ctx, cfg, dataDir, "/access/revoke", url.Values{"name": {name}}); err != nil {
			return fmt.Errorf("revoke the key through the running daemon: %w", err)
		}
		fmt.Printf("Access key %q revoked; it stops working immediately.\n", name)
		return nil
	}
	live := manager.Snapshot()
	if _, ok := live.AccessKey(name); !ok {
		return fmt.Errorf("access key %q does not exist", name)
	}
	live.RemoveAccessKey(name)
	if err := live.ValidateRuntime(); err != nil {
		return fmt.Errorf("refusing to remove the last key while listening beyond loopback: %w", err)
	}
	if err := manager.Update(live); err != nil {
		return err
	}
	fmt.Printf("Access key %q removed. SMR is not running; restart it to apply the change.\n", name)
	return nil
}

func loadOfflineManager() (*config.Manager, string, string, error) {
	configPath, dataDir, err := paths()
	if err != nil {
		return nil, "", "", err
	}
	manager, err := config.LoadManager(configPath)
	if err != nil {
		return nil, "", "", err
	}
	return manager, configPath, dataDir, nil
}

func daemonAlive(ctx context.Context, cfg config.Config) bool {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := service.HealthCheck(probe, "http://"+cfg.ConnectAddr())
	return err == nil
}

// uiAlive is separate from daemonAlive on purpose. If the proxy answers but
// the setup UI does not, editing the file behind the daemon's back would make
// `keys revoke` report success while the running process keeps the old key.
func uiAlive(ctx context.Context, cfg config.Config) bool {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, "http://"+cfg.UIConnectAddr()+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func fetchKeyStats(ctx context.Context, cfg config.Config) ([]proxy.KeyStats, error) {
	key, ok := cfg.LocalAccessKey()
	if !ok {
		return nil, fmt.Errorf("no access key")
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, "http://"+cfg.ConnectAddr()+"/stats", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key.Key)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stats returned %d", resp.StatusCode)
	}
	var stats proxy.Stats
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&stats); err != nil {
		return nil, err
	}
	return stats.Keys, nil
}

func uiAccessRequest(ctx context.Context, cfg config.Config, dataDir, path string, form url.Values) (map[string]any, error) {
	token, err := ui.ReadTokenFile(dataDir)
	if err != nil {
		return nil, fmt.Errorf("read UI token: %w", err)
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodPost,
		"http://"+cfg.UIConnectAddr()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-SMR-UI-Token", token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("setup UI returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode setup UI response: %w", err)
	}
	return result, nil
}

func printKeyInstructions(cfg config.Config, key config.AccessKey, advertised string) error {
	if strings.TrimSpace(advertised) == "" {
		advertised = cfg.AdvertiseURL()
	}
	for _, host := range []string{hostcfg.Codex, hostcfg.Claude} {
		instructions, err := hostcfg.InstructionsFor(host, advertised, key.Key)
		if err != nil {
			return err
		}
		fmt.Printf("\n%s (%s):\n", hostLabel(host), instructions.Path)
		for _, part := range instructions.Parts {
			fmt.Println(part.Code)
			fmt.Println()
		}
	}
	if cfg.IsLoopbackListen() {
		fmt.Println("To let another device connect, set `listen: 0.0.0.0:8787` in the SMR config and restart it.")
	}
	return nil
}

func hostLabel(host string) string {
	if host == hostcfg.Claude {
		return "Claude Code"
	}
	return "Codex"
}
