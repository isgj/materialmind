package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"materialmind/internal/acpinternal"
	"materialmind/internal/credentialstore"
	"materialmind/internal/engine"
	"materialmind/internal/httpapi"
	"materialmind/internal/httpserver"
	"materialmind/internal/mcpruntime"
	"materialmind/internal/store"
	"materialmind/internal/webui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("materialmind stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "mcp-bridge" {
		return runMCPBridge(args[1:])
	}
	if len(args) > 0 && args[0] == "acp-session-mcp" {
		return runACPInternalMCP(args[1:])
	}
	flags := flag.NewFlagSet("materialmind", flag.ContinueOnError)
	address := flags.String("addr", envOrDefault("MATERIALMIND_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	publicURL := flags.String("public-url", os.Getenv("MATERIALMIND_PUBLIC_URL"), "public HTTP origin used for OAuth callbacks")
	allowRemote := flags.Bool("allow-remote", false, "allow listening on a non-loopback address")
	dataDirectory := flags.String("data-dir", envOrDefault("MATERIALMIND_DATA_DIR", defaultDataDirectory()), "directory for persistent application data")
	credentialStoreMode := flags.String("credential-store", envOrDefault("MATERIALMIND_CREDENTIAL_STORE", "auto"), "credential storage: auto, keyring, or memory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %q", flags.Args())
	}
	if err := httpserver.ValidateListenAddress(*address, *allowRemote); err != nil {
		return err
	}
	resolvedPublicURL, err := httpserver.ResolvePublicOrigin(*address, *publicURL)
	if err != nil {
		return err
	}
	internalOrigin, err := httpserver.ResolvePublicOrigin(*address, "")
	if err != nil {
		return err
	}
	credentials, err := credentialstore.New(*credentialStoreMode)
	if err != nil {
		return err
	}

	ctx := context.Background()
	databasePath := filepath.Join(*dataDirectory, "materialmind.db")
	dataStore, err := store.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer dataStore.Close()
	if err := dataStore.InterruptRunningRuns(ctx); err != nil {
		return fmt.Errorf("recover interrupted runs: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve MaterialMind executable: %w", err)
	}
	runEngine := engine.NewWithOptions(dataStore, engine.Options{
		Credentials:           credentials,
		MCPCallbackURL:        resolvedPublicURL + "/api/mcp-oauth/callback",
		MCPBridgeCommand:      executable,
		DatabasePath:          databasePath,
		CredentialStoreMode:   *credentialStoreMode,
		ACPInternalMCPURL:     internalOrigin + "/api/internal/acp-session-tools",
		ACPInternalMCPCommand: executable,
	})
	if deleted, err := runEngine.ApplyRetention(ctx); err != nil {
		return fmt.Errorf("apply session retention: %w", err)
	} else if deleted > 0 {
		slog.Info("deleted expired sessions", "count", deleted)
	}
	apiHandler := httpapi.New(dataStore, runEngine)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	if frontend, ok := webui.Handler(); ok {
		mux.Handle("/", frontend)
	} else {
		mux.HandleFunc("/", frontendNotEmbedded)
	}
	handler, err := httpserver.Middleware(mux, resolvedPublicURL)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              *address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go runRetention(shutdownContext, runEngine)
	serverErrors := make(chan error, 1)
	slog.Info("materialmind listening", "addr", *address, "public_url", resolvedPublicURL, "data_dir", *dataDirectory)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-shutdownContext.Done():
	}

	slog.Info("materialmind shutting down")
	graceContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runEngine.Shutdown(graceContext); err != nil {
		return fmt.Errorf("stop agent engine: %w", err)
	}
	if err := server.Shutdown(graceContext); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

func runACPInternalMCP(args []string) error {
	flags := flag.NewFlagSet("materialmind acp-session-mcp", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected acp-session-mcp arguments: %q", flags.Args())
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	return acpinternal.RunServer(
		ctx,
		os.Getenv(acpinternal.EndpointEnvironment),
		os.Getenv(acpinternal.TokenEnvironment),
	)
}

func runRetention(ctx context.Context, runEngine *engine.Engine) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := runEngine.ApplyRetention(ctx)
			if err != nil {
				slog.Error("apply session retention", "error", err)
			} else if deleted > 0 {
				slog.Info("deleted expired sessions", "count", deleted)
			}
		}
	}
}

func runMCPBridge(args []string) error {
	flags := flag.NewFlagSet("materialmind mcp-bridge", flag.ContinueOnError)
	databasePath := flags.String("database", "", "MaterialMind database path")
	encodedServer := flags.String("server", "", "encoded MCP server snapshot")
	credentialStoreMode := flags.String("credential-store", "auto", "credential storage: auto, keyring, or memory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *databasePath == "" || *encodedServer == "" {
		return fmt.Errorf("mcp-bridge requires --database and --server")
	}
	rawServer, err := base64.RawURLEncoding.DecodeString(*encodedServer)
	if err != nil {
		return fmt.Errorf("decode MCP bridge server: %w", err)
	}
	var server store.MCPServer
	if err := json.Unmarshal(rawServer, &server); err != nil {
		return fmt.Errorf("parse MCP bridge server: %w", err)
	}
	if server.Transport != store.MCPTransportHTTP ||
		server.AuthType != store.MCPAuthOAuth ||
		server.ID == "" {
		return fmt.Errorf("mcp-bridge requires an OAuth HTTP MCP server")
	}
	credentials, err := credentialstore.New(*credentialStoreMode)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	dataStore, err := store.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer dataStore.Close()
	manager := mcpruntime.New(dataStore, mcpruntime.Options{
		Credentials: credentials,
	})
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownContext)
	}()
	return manager.RunStdioBridge(ctx, server)
}

func frontendNotEmbedded(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "frontend is not embedded; run the Angular development server or build with -tags embed_frontend", http.StatusNotFound)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func defaultDataDirectory() string {
	directory, err := os.UserConfigDir()
	if err != nil {
		return ".materialmind"
	}
	return filepath.Join(directory, "materialmind")
}
