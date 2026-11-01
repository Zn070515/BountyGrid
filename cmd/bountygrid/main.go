package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Zn070515/BountyGrid/internal/obs"
	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	loadEnvFile(".env.local")
	obs.Setup(os.Stderr)
	slog.Debug("command", "name", os.Args[1])

	base, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "migrate":
		if err := runMigrate(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		fmt.Println("migrations applied")
	case "ping":
		if err := runPing(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "ping:", err)
			os.Exit(1)
		}
		fmt.Println("postgres ok")
	case "rollback":
		if err := runRollback(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "rollback:", err)
			os.Exit(1)
		}
	case "sync":
		syncCtx, cancel := context.WithTimeout(base, 10*time.Minute)
		defer cancel()
		if err := runSync(syncCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sync:", err)
			os.Exit(1)
		}
	case "derive":
		deriveCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runDerive(deriveCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "derive:", err)
			os.Exit(1)
		}
	case "decide":
		decideCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runDecide(decideCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "decide:", err)
			os.Exit(1)
		}
	case "schedule":
		scheduleCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runSchedule(scheduleCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "schedule:", err)
			os.Exit(1)
		}
	case "competition":
		compCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runCompetition(compCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "competition:", err)
			os.Exit(1)
		}
	case "harness":
		harnessCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runHarness(harnessCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "harness:", err)
			os.Exit(1)
		}
	case "model":
		if err := runModel(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "model:", err)
			os.Exit(1)
		}
	case "verify":
		verifyCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runVerify(verifyCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "verify:", err)
			os.Exit(1)
		}
	case "report":
		reportCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runReport(reportCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			os.Exit(1)
		}
	case "submit":
		submitCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runSubmit(submitCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "submit:", err)
			os.Exit(1)
		}
	case "ledger":
		ledgerCtx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		if err := runLedger(ledgerCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ledger:", err)
			os.Exit(1)
		}
	case "trace":
		if err := runTrace(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "trace:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bountygrid <migrate|rollback|ping|sync|derive|decide|schedule|competition|harness|model|verify|report|submit|ledger|trace|version>")
}

func dsn() (string, error) {
	v := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if v == "" {
		return "", fmt.Errorf("DATABASE_URL is empty (set it or create .env.local)")
	}
	return v, nil
}

func runPing(ctx context.Context) error {
	d, err := dsn()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, d)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.Ping(ctx)
}

func runRollback(ctx context.Context) error {
	d, err := dsn()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, d)
	if err != nil {
		return err
	}
	defer pool.Close()
	version, err := postgres.RollbackLast(ctx, pool)
	if err != nil {
		return err
	}
	fmt.Println("rolled back", version)
	return nil
}

func runMigrate(ctx context.Context) error {
	d, err := dsn()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, d)
	if err != nil {
		return err
	}
	defer pool.Close()
	return postgres.Migrate(ctx, pool)
}

// loadEnvFile sets variables from a simple KEY=VALUE file without overriding
// existing environment variables. Values are never printed.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
