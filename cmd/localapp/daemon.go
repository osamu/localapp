package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/osamu/localapp/internal/ca"
	"github.com/osamu/localapp/internal/config"
	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/dashboard"
	"github.com/osamu/localapp/internal/dnsd"
	"github.com/osamu/localapp/internal/proxy"
	"github.com/osamu/localapp/internal/registry"
)

// loopback is the bind address of every listener. Nothing listens on
// `0.0.0.0` (DESIGN.md "セキュリティ", listener row).
const loopback = "127.0.0.1"

// shutdownTimeout is how long a graceful shutdown is awaited.
const shutdownTimeout = 5 * time.Second

// cmdDaemon starts every listener (DNS / HTTPS / HTTP / control) in the
// foreground (DESIGN.md "アーキテクチャ"). It is launched by launchd or
// systemd.
func cmdDaemon(args []string) int {
	fs := newFlagSet("daemon")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp daemon")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) != 0 {
		fs.Usage()
		return exitUsage
	}

	cfg := loadConfig()
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return reportError(fmt.Errorf("creating the state directory (%s): %w", cfg.StateDir, err))
	}

	logFile, logger, err := openDaemonLog(cfg.LogPath())
	if err != nil {
		return reportError(err)
	}
	defer logFile.Close()

	if err := runDaemon(cfg, logger); err != nil {
		logger.Printf("error: %v", err)
		return reportError(err)
	}
	logger.Printf("stopped")
	return exitOK
}

// openDaemonLog opens <state>/daemon.log for appending. When running in the
// foreground (stderr is a terminal) it also writes to stderr.
func openDaemonLog(path string) (*os.File, *log.Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open the log file (%s): %w", path, err)
	}
	var w io.Writer = f
	if isTerminal(os.Stderr) {
		w = io.MultiWriter(f, os.Stderr)
	}
	return f, log.New(w, "", log.LstdFlags), nil
}

// isTerminal reports whether f is a terminal. Under launchd it is false.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// runDaemon opens every listener before serving. If even one cannot be opened
// it starts nothing and returns an error (to avoid a partial startup).
func runDaemon(cfg config.Config, logger *log.Logger) error {
	store, err := registry.Open(cfg.RegistryPath())
	if err != nil {
		return err
	}
	rootCA, err := ca.LoadOrCreate(cfg.CADir(), cfg.CertsDir(), cfg.Domain)
	if err != nil {
		return err
	}
	dnsSrv, err := dnsd.New(cfg.Domain)
	if err != nil {
		return err
	}

	// --- Open the listeners (on failure, close the ones already opened) ---
	var opened []io.Closer
	closeAll := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			opened[i].Close()
		}
	}

	controlLn, err := control.Listen(cfg.SocketPath)
	if err != nil {
		return err
	}
	opened = append(opened, controlLn)

	dnsLn, err := dnsd.Listen(net.JoinHostPort(loopback, strconv.Itoa(cfg.DNSPort)))
	if err != nil {
		closeAll()
		return listenError("DNS", cfg.DNSPort, err)
	}
	opened = append(opened, dnsLn)

	httpsLn, err := net.Listen("tcp", net.JoinHostPort(loopback, strconv.Itoa(cfg.HTTPSPort)))
	if err != nil {
		closeAll()
		return listenError("HTTPS", cfg.HTTPSPort, err)
	}
	opened = append(opened, httpsLn)

	httpLn, err := net.Listen("tcp", net.JoinHostPort(loopback, strconv.Itoa(cfg.HTTPPort)))
	if err != nil {
		closeAll()
		return listenError("HTTP", cfg.HTTPPort, err)
	}
	opened = append(opened, httpLn)

	defer os.Remove(cfg.SocketPath)

	// Hand the socket and the log to the installing user (whoever ran
	// sudo localapp install).
	applyOwner(cfg, logger)

	// --- Handlers ---
	dash := dashboard.New(store, dashboard.Options{
		Domain:    cfg.Domain,
		Version:   config.Version,
		Listeners: cfg.Listeners(),
	})
	px := proxy.New(store, proxy.Options{
		Domain:    cfg.Domain,
		ErrorLog:  logger,
		Dashboard: dash,
	})
	controlSrv := control.NewServer(store, control.Options{
		Domain:    cfg.Domain,
		Version:   config.Version,
		Listeners: cfg.Listeners(),
		StartedAt: time.Now(),
	})
	// No timeout is set: the first compile of a dev server can take tens of
	// seconds (DESIGN.md "プロキシ実装要件").
	httpsSrv := &http.Server{Handler: px, ErrorLog: logger, TLSConfig: rootCA.TLSConfig()}
	httpSrv := &http.Server{
		Handler:           redirectHandler(cfg.HTTPSPort),
		ErrorLog:          logger,
		ReadHeaderTimeout: 10 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// If any listener dies, stop everything.
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	apps, services := store.Counts()
	logger.Printf("localapp %s started (domain=%s apps=%d services=%d)", config.Version, cfg.Domain, apps, services)
	logger.Printf("listener dns=%s https=%s http=%s control=%s",
		dnsLn.Addr(), httpsLn.Addr(), httpLn.Addr(), cfg.SocketPath)
	logger.Printf("root CA: %s", rootCA.CertPath())

	// --- serve ---
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	run := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			if err := fn(); err != nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}

	run("control", func() error { return control.Serve(ctx, controlLn, controlSrv) })
	run("dns", func() error { return dnsSrv.Serve(ctx, dnsLn) })
	run("https", func() error { return serveHTTP(ctx, httpsSrv, httpsLn, true) })
	run("http", func() error { return serveHTTP(ctx, httpSrv, httpLn, false) })

	wg.Wait()
	close(errs)
	return <-errs
}

// serveHTTP runs srv on ln and shuts it down gracefully when ctx is cancelled.
// With useTLS it uses ServeTLS (enabling SNI issuance from srv.TLSConfig and
// HTTP/2).
func serveHTTP(ctx context.Context, srv *http.Server, ln net.Listener, useTLS bool) error {
	errCh := make(chan error, 1)
	go func() {
		if useTLS {
			errCh <- srv.ServeTLS(ln, "", "")
			return
		}
		errCh <- srv.Serve(ln)
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// redirectHandler sends HTTP to HTTPS with a 301. The Host keeps its original
// value, and the Location includes the port when the HTTPS port is not 443.
func redirectHandler(httpsPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" {
			http.Error(w, "missing Host header", http.StatusBadRequest)
			return
		}
		if httpsPort != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(httpsPort))
		} else if isIPv6Literal(host) {
			host = "[" + host + "]"
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// isIPv6Literal reports whether the host is an IPv6 literal without brackets.
func isIPv6Literal(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() == nil
}

// listenError turns a bind failure into an error that states the cause and
// what to do about it.
func listenError(name string, port int, err error) error {
	msg := fmt.Errorf("cannot open the %s listener (127.0.0.1:%d): %w", name, port, err)
	switch {
	case isPermissionError(err) && port < 1024 && os.Geteuid() != 0:
		return fmt.Errorf("%w\n  ports below 1024 require root privileges. "+
			"Either install the service with `sudo localapp install`,\n"+
			"  or pick ports of 1024 or above via LOCALAPP_HTTP_PORT / LOCALAPP_HTTPS_PORT / LOCALAPP_DNS_PORT", msg)
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Errorf("%w\n  another process is already using it. "+
			"Check with `sudo lsof -nP -iTCP:%d -sTCP:LISTEN`, or change the port via the environment variables", msg, port)
	default:
		return msg
	}
}

func isPermissionError(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, os.ErrPermission)
}

// applyOwner hands the control socket and the log to the installing user when
// one was recorded, so that the CLI can use socket(0600) even though the daemon
// runs as root (DESIGN.md "状態ディレクトリのレイアウト": control.sock is owned
// by the installing user).
func applyOwner(cfg config.Config, logger *log.Logger) {
	uid, gid, ok := readOwner(cfg.StateDir)
	if !ok || os.Geteuid() != 0 {
		return
	}
	for _, path := range []string{cfg.SocketPath, cfg.LogPath()} {
		if err := os.Chown(path, uid, gid); err != nil {
			logger.Printf("warning: cannot change the owner (%s → %d:%d): %v", path, uid, gid, err)
		}
	}
	logger.Printf("owner of the control socket: uid=%d gid=%d", uid, gid)
}
