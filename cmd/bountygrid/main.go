package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Zn070515/BountyGrid/internal/store/postgres"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	loadEnvFile(".env.local")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
		syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := runSync(syncCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sync:", err)
			os.Exit(1)
		}
	case "derive":
		deriveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := runDerive(deriveCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "derive:", err)
			os.Exit(1)
		}
	case "decide":
		decideCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := runDecide(decideCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "decide:", err)
			os.Exit(1)
		}
	case "schedule":
		scheduleCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := runSchedule(scheduleCtx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "schedule:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("bountygrid dev")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bountygrid <migrate|rollback|ping|sync|derive|decide|schedule|version>")
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
