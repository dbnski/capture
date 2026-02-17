package main

import (
    "context"
    "database/sql"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "path/filepath"
    "strings"
    "syscall"
    "time"

    "github.com/alecthomas/kong"
    "github.com/go-sql-driver/mysql"
    "github.com/samber/lo"
    "golang.org/x/term"
)

var options struct {
    Hostname      string        `name:"hostname" 
                                 placeholder:"ADDRESS" 
                                 help:"Database address (required)"`
    Username      string        `name:"username" 
                                 placeholder:"USERNAME" 
                                 help:"Database user"`
    Password      string        `name:"password" 
                                 xor:"password" 
                                 placeholder:"PASSWORD" 
                                 help:"Database password"`
    Port          string        `name:"port" 
                                 placeholder:"PORT" 
                                 default:"3306" 
                                 help:"Database port"`
    TLS           bool          `name:"tls" 
                                 help:"Use TLS connection to database"`
    Socket        string        `name:"socket" 
                                 placeholder:"PATH" 
                                 default:"/var/run/mysqld/mysqld.sock" 
                                 help:"Database unix socket"`
    AskPass       bool          `name:"ask-pass" 
                                 xor:"password" 
                                 help:"Prompt for a password"`
    DefaultsFile  string        `name:"defaults-file" 
                                 placeholder:"FILE" 
                                 help:"Default database options file"`
    DefaultsGroup string        `name:"defaults-group" 
                                 placeholder:"NAME" 
                                 default:"client" 
                                 help:"Defaults file section name"`
    Interval      time.Duration `name:"interval" 
                                 placeholder:"DURATION" 
                                 default:"5s" 
                                 help:"Interval for collecting data"`
    Path          string        `name:"path" 
                                 placeholder:"PATH" 
                                 default:"." 
                                 help:"Output directory"`
    Tasks         []string      `name:"tasks" 
                                 placeholder:"NAME" 
                                 enum:"${tasks}" 
                                 sep:"," 
                                 help:"Capture tasks to run: ${tasks} (default: all)"`
    Version       bool          `name:"version" 
                                 short:"v" 
                                 help:"Print version information"`
}

var (
    Version    = "0.0.0"
    CommitHash = "0000000"
    Build      = "dev"
    BuildTime  = "(recently)"
)

func main() {
    kong.Parse(
        &options,
        kong.Description(
            fmt.Sprintf("Version: %s-%s.%s %s", Version, Build, CommitHash, BuildTime)),
        kong.UsageOnError(),
        kong.Vars{
            "tasks": strings.Join(lo.Map(allTasks, func(t captureTask, _ int) string { return t.name }), ","),
        },
    )

    if options.Version {
        fmt.Println(Version)
        os.Exit(0)
    }

    slog.Info("Version", "version", Version, "build", Build, "commit", CommitHash)

    if options.Hostname == "" {
        slog.Error("--hostname is required")
        os.Exit(1)
    }

    config, err := buildMySQLConfig()
    if err != nil {
        slog.Error("Failed to load defaults file", "file", options.DefaultsFile, "error", err)
        os.Exit(1)
    }

    if err := validatePathIsWriteable(options.Path); err != nil {
        slog.Error("Invalid output path", "path", options.Path, "error", err)
        os.Exit(1)
    }
    fullPath, _ := filepath.Abs(options.Path)
    slog.Info("Output path", "path", fullPath)

    db, err := getDatabase(config)
    if err != nil {
        slog.Error("Failed to open database connection", "error", err)
        os.Exit(1)
    }
    defer db.Close()

    ctx, cancel := getContext(context.Background())
    defer cancel()

    slog.Info("Capturing state information", "interval", options.Interval)

    tasks := lo.Filter(allTasks, func(t captureTask, _ int) bool {
        return len(options.Tasks) == 0 || lo.Contains(options.Tasks, t.name)
    })

    errCh := make(chan error, len(tasks))
    for _, task := range tasks {
        go func(t captureTask) {
            errCh <- capture(ctx, db, t.name, t.fn)
        }(task)
    }

    for range tasks {
        if err := <-errCh; err != nil {
            cancel()
        }
    }

    slog.Info("Shutting down")
}

func buildMySQLConfig() (*mysql.Config, error) {
    config := &mysql.Config{
        User:                 options.Username,
        Passwd:               options.Password,
        Timeout:              5 * time.Second,
        ReadTimeout:          5 * time.Second,
        WriteTimeout:         5 * time.Second,
        AllowNativePasswords: true,
    }

    if options.Hostname == "" || options.Hostname == "localhost" {
        config.Net  = "unix"
        config.Addr = options.Socket
    } else {
        config.Net  = "tcp"
        config.Addr = options.Hostname + ":" + options.Port
    }

    if options.TLS {
        config.TLSConfig = "skip-verify"
    }

    if options.DefaultsFile != "" {
        if err := loadConfigFile(config, options.DefaultsFile, options.DefaultsGroup); err != nil {
            return nil, err
        }
    }

    if options.AskPass {
        slog.Info("Password: ")
        passwd, err := term.ReadPassword(int(syscall.Stdin))
        if err != nil {
            return nil, fmt.Errorf("failed to read password: %w", err)
        }
        config.Passwd = string(passwd)
    }

    return config, nil
}

func getDatabase(config *mysql.Config) (*sql.DB, error) {
    db, err := sql.Open("mysql", config.FormatDSN())
    if err != nil {
        return nil, err
    }
    db.SetConnMaxLifetime(24 * time.Hour)
    db.SetMaxOpenConns(3)

    if err := db.Ping(); err != nil {
        return nil, err
    }

    return db, nil
}

func getContext(ctx context.Context) (context.Context, context.CancelFunc) {
    ctx, cancel := context.WithCancel(ctx)

    signals := make(chan os.Signal, 1)
    signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)

    go func() {
        select {
        case <-signals:
            cancel()
        case <-ctx.Done():
        }
    }()

    return ctx, func() {
        signal.Stop(signals)
        cancel()
    }
}

func validatePathIsWriteable(path string) error {
    stat, err := os.Stat(path)
    if err != nil {
        return err
    }
    if !stat.IsDir() {
        return fmt.Errorf("not a directory")
    }
    testFile := path + "/.test"
    f, err := os.Create(testFile)
    if err != nil {
        return fmt.Errorf("not writable: %w", err)
    }
    f.Close()
    os.Remove(testFile)
    return nil
}