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
	"net/http"
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
		err = service.Restart(ctx)
	case "update":
		err = runUpdate(ctx, os.Args[2:])
	case "uninstall":
		err = runUninstall(ctx, os.Args[2:])
	case "migrate-config":
		err = runMigrate()
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
  smr uninstall [--restore] [--purge]
  smr migrate-config
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

	router := proxy.New(manager, logger)
	setupUI, err := ui.New(manager, router.Snapshot, dataDir, token, version, logger)
	if err != nil {
		return err
	}
	cfg := manager.Snapshot()
	proxyServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	uiServer := &http.Server{
		Addr:              cfg.UIListen,
		Handler:           setupUI.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() {
		logger.Info("SMR listening", "addr", cfg.Listen, "version", version)
		errCh <- proxyServer.ListenAndServe()
	}()
	go func() {
		logger.Info("setup UI listening", "addr", cfg.UIListen)
		errCh <- uiServer.ListenAndServe()
	}()
	fmt.Printf("SMR setup: http://%s/?token=%s\n", cfg.UIListen, token)

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
	if _, err := service.HealthCheck(ctx, "http://"+cfg.Listen); err != nil {
		return fmt.Errorf("SMR is not running; start the background service or run `smr serve` first")
	}
	token, err := ui.ReadTokenFile(dataDir)
	if err != nil {
		return fmt.Errorf("setup token not found; restart SMR, then run `smr setup` again")
	}
	target := fmt.Sprintf("http://%s/?token=%s", cfg.UIListen, token)
	fmt.Println("Opening SMR setup:", target)
	if err := service.OpenBrowser(ctx, target); err != nil {
		fmt.Println("Open this URL manually:", target)
	}
	return nil
}

func runStatus(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	health, err := service.HealthCheck(ctx, "http://"+cfg.Listen)
	if err != nil {
		return fmt.Errorf("SMR is not reachable at %s: %w", cfg.Listen, err)
	}
	body, _ := json.MarshalIndent(health, "", "  ")
	fmt.Println(string(body))
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
	if _, err := service.HealthCheck(ctx, "http://"+cfg.Listen); err != nil {
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
	if err := service.Restart(ctx); err != nil {
		fmt.Println("restart the installed service manually:", err)
	}
	return nil
}

func runUninstall(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	restore := flags.Bool("restore", false, "restore host config from backups")
	purge := flags.Bool("purge", false, "remove SMR config and local state")
	if err := flags.Parse(args); err != nil {
		return err
	}
	configPath, dataDir, err := paths()
	if err != nil {
		return err
	}
	if *restore {
		for _, host := range []string{hostcfg.Codex, hostcfg.Claude} {
			path, err := hostcfg.RestoreLatest(host, dataDir)
			if err != nil {
				fmt.Printf("restore %s: skipped (%v)\n", host, err)
				continue
			}
			fmt.Println("restored", path)
		}
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
