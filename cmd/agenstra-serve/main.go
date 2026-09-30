package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "agenstra-serve:", e)
		os.Exit(2)
	}
}
func run() error {
	config := flag.String("config", os.Getenv("AGENT_DEPLOYMENT_CONFIG"), "deployment JSON")
	address := flag.String("host", "127.0.0.1", "listen address")
	port := flag.Int("port", 8091, "listen port")
	flag.Parse()
	if *config == "" {
		return errors.New("--config or AGENT_DEPLOYMENT_CONFIG is required")
	}
	dep, e := agenstra.LoadDeployment(*config)
	if e != nil {
		return e
	}
	store, e := agenstra.NewSQLiteStore(dep.DatabasePath())
	if e != nil {
		return e
	}
	defer store.Close()
	if dep.Registry != nil {
		defer dep.Registry.Close()
	}
	settings := dep.Config.Settings
	model, e := agenstra.NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), time.Duration(settings.ModelTimeoutSeconds*float64(time.Second)), nil)
	if e != nil {
		return e
	}
	defer model.Close()
	host := agenstra.NewAgentHost(store, dep.ProviderFactory, model, dep.PolicyResolver)
	host.Settings = settings
	if dep.Registry != nil {
		host.ReleaseResolver = dep.ReleaseResolver
		host.ReleaseProviderFactory = dep.ReleaseProviderFactory
	}
	server, e := agenstra.NewHTTPServer(host, dep, true, time.Second)
	if e != nil {
		return e
	}
	defer server.Close()
	httpServer := &http.Server{Addr: *address + ":" + strconv.Itoa(*port), Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	fmt.Printf("Agenstra listening on %s\n", httpServer.Addr)
	e = httpServer.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
