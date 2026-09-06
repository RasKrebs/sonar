package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/raskrebs/sonar/internal/relay"
	"github.com/spf13/cobra"
)

var (
	relayListen      string
	relayDB          string
	relayDatabaseURL string
	relayProjectKeys []string
	relayRetention   int
	relayLogLevel    string
)

var relayServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the relay's HTTP service",
	Long: `Run the relay's HTTP service.

It binds all interfaces by default because it runs in a container, and it
speaks plain HTTP: put a TLS-terminating proxy in front of it. docs/RELAY.md
has a Caddy file and a one-command deploy.

Routes:
  POST /v1/events   accept a batch of anonymous events
  GET  /healthz     liveness, never authenticated
  GET  /v1/stats    active installs and events per name per day

Storage is SQLite by default and Postgres when --database-url (or DATABASE_URL)
is set. Every flag has an environment equivalent, which is what a container
actually uses:

  --listen         SONAR_RELAY_LISTEN
  --db             SONAR_RELAY_DB
  --database-url   DATABASE_URL
  --project-keys   SONAR_RELAY_PROJECT_KEYS   (comma-separated)
  --retention-days SONAR_RELAY_RETENTION_DAYS

With --project-keys set, /v1/events and /v1/stats require one of the keys in
X-Sonar-Key or 'Authorization: Bearer'. With none set the relay is open, which
is only safe on a private network.

Examples:
  sonar relay serve
  sonar relay serve --listen :9000 --db /data/relay.db --retention-days 30
  sonar relay serve --database-url postgres://relay@db/relay --project-keys k1,k2`,
	Args: cobra.NoArgs,
	RunE: runRelayServe,
}

func init() {
	relayServeCmd.Flags().StringVar(&relayListen, "listen", "",
		"Address to bind (default "+relay.DefaultListen+", env SONAR_RELAY_LISTEN)")
	relayServeCmd.Flags().StringVar(&relayDB, "db", "",
		"SQLite database file (default ./relay.db, env SONAR_RELAY_DB)")
	relayServeCmd.Flags().StringVar(&relayDatabaseURL, "database-url", "",
		"Postgres URL; when set it replaces SQLite (env DATABASE_URL)")
	relayServeCmd.Flags().StringSliceVar(&relayProjectKeys, "project-keys", nil,
		"Keys that authorise /v1 requests; repeat or comma-separate (env SONAR_RELAY_PROJECT_KEYS)")
	relayServeCmd.Flags().IntVar(&relayRetention, "retention-days", 0,
		"Days of raw events to keep; rollups are kept forever (default 90, env SONAR_RELAY_RETENTION_DAYS)")
	relayServeCmd.Flags().StringVar(&relayLogLevel, "log-level", "info",
		"Log level: debug, info, warn or error")
	relayCmd.AddCommand(relayServeCmd)
}

// DefaultRelayDB is where the relay keeps its SQLite file when nothing says
// otherwise: the working directory, which in the container is the mounted
// volume. It deliberately does not go near ~/.config/sonar — that belongs to
// the daemon, and the relay is not a sonar install.
const DefaultRelayDB = "relay.db"

func runRelayServe(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true

	listen := firstNonEmpty(relayListen, os.Getenv("SONAR_RELAY_LISTEN"), relay.DefaultListen)
	dbPath := firstNonEmpty(relayDB, os.Getenv("SONAR_RELAY_DB"), DefaultRelayDB)
	databaseURL := firstNonEmpty(relayDatabaseURL, os.Getenv("DATABASE_URL"))

	keys := relayProjectKeys
	if len(keys) == 0 {
		keys = splitKeys(os.Getenv("SONAR_RELAY_PROJECT_KEYS"))
	}

	retention, err := relayRetentionDuration()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: relayLogLevelValue(relayLogLevel),
	}))

	store, err := relay.Open(databaseURL, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	srv, err := relay.New(relay.Options{
		Listen:      listen,
		Store:       store,
		ProjectKeys: keys,
		Retention:   retention,
		Logger:      logger,
	})
	if err != nil {
		return err
	}

	backend := "sqlite:" + dbPath
	if databaseURL != "" {
		backend = "postgres"
	}
	logger.Info("relay starting", "backend", backend)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx)
}

// relayRetentionDuration resolves --retention-days, then
// SONAR_RELAY_RETENTION_DAYS, then the package default.
func relayRetentionDuration() (time.Duration, error) {
	days := relayRetention
	if days == 0 {
		if v := strings.TrimSpace(os.Getenv("SONAR_RELAY_RETENTION_DAYS")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf("SONAR_RELAY_RETENTION_DAYS is %q, which is not a number of days", v)
			}
			days = n
		}
	}
	if days == 0 {
		return relay.DefaultRetention, nil
	}
	if days < 1 {
		return 0, fmt.Errorf("--retention-days must be at least 1, got %d", days)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func relayLogLevelValue(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func splitKeys(v string) []string {
	var out []string
	for _, k := range strings.Split(v, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
