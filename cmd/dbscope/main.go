// Command dbscope runs the local agent and its web UI.
package main

import (
	"context"
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
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/pkg/browser"

	"github.com/dbscope/dbscope/internal/agent"
	"github.com/dbscope/dbscope/internal/logx"
	"github.com/dbscope/dbscope/internal/secrets"
	"github.com/dbscope/dbscope/internal/store"
	"github.com/dbscope/dbscope/web"
)

const defaultPort = 4555

const usage = `usage:
  dbscope ui [--port 4555] [--no-open] [--dev-origin URL]
  dbscope eval nl --dataset DIR --dsn URL [--provider P --model M --base-url U] [--limit N] [--json]
  dbscope migrate check PATH... [--dsn URL | --schema-dump FILE [--stats FILE]] [--fail-on high] [--format text|json|sarif|github]
  dbscope migrate capture --shadow URL [--dsn URL] [--out DIR] -- COMMAND...
  dbscope stats export --dsn URL [--schemas a,b]
  dbscope share export|import --connection NAME [--out FILE | FILE]
  dbscope version
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "ui":
		return ui(args[1:])
	case "eval":
		return evalCmd(args[1:], os.Stdout, os.Stderr)
	case "migrate":
		return migrateCmd(args[1:], os.Stdout, os.Stderr)
	case "stats":
		return statsCmd(args[1:], os.Stdout, os.Stderr)
	case "share":
		return shareCmd(args[1:], os.Stdout, os.Stderr)
	case "version":
		fmt.Printf("dbscope %s %s/%s\n", agent.Version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "dbscope: unknown command %q\n%s", args[0], usage)
		return 2
	}
}

func ui(args []string) int {
	flags := flag.NewFlagSet("ui", flag.ContinueOnError)
	port := flags.Int("port", defaultPort, "port to listen on (127.0.0.1 only)")
	noOpen := flags.Bool("no-open", false, "do not open a browser")
	devOrigin := flags.String("dev-origin", "", "also accept requests from this origin, e.g. http://localhost:5173 (Vite dev server)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "dbscope ui: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	portSet := false
	flags.Visit(func(f *flag.Flag) { portSet = portSet || f.Name == "port" })

	log := logx.New(os.Stderr, slog.LevelInfo)
	if err := serve(log, *port, portSet, *noOpen, *devOrigin); err != nil {
		fmt.Fprintf(os.Stderr, "dbscope: %v\n", err)
		return 1
	}
	return 0
}

func serve(log *slog.Logger, port int, portSet, noOpen bool, devOrigin string) error {
	if devOrigin != "" {
		if u, err := url.Parse(devOrigin); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
			return fmt.Errorf("--dev-origin must be an origin like http://localhost:5173")
		}
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("--port must be 1-65535")
	}

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil && !portSet {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	defer ln.Close()
	port = ln.Addr().(*net.TCPAddr).Port

	dir, err := configDir()
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dir, "dbscope.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	vault, err := secrets.Open(filepath.Join(dir, "secrets.age"))
	if err != nil {
		return err
	}

	token, err := agent.NewToken()
	if err != nil {
		return err
	}
	h, err := agent.New(agent.Config{
		Port: port, Token: token, DevOrigin: devOrigin,
		Assets: web.Assets(), Store: st, Secrets: vault, Logger: log,
	})
	if err != nil {
		return err
	}
	// Deferred after st.Close, so it runs first: pools close before the store.
	defer h.Close()
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	link := fmt.Sprintf("http://127.0.0.1:%d/#t=%s", port, token)
	fmt.Printf("dbscope running at %s\n", link)
	if !noOpen {
		browser.Stdout, browser.Stderr = io.Discard, io.Discard
		if err := browser.OpenURL(link); err != nil {
			fmt.Println("Could not open a browser; open the URL above.")
		}
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	stop() // a second Ctrl+C now kills the process
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
