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
                                 group:"Database" 
                                 placeholder:"HOSTNAME" 
                                 default:"localhost" 
                                 help:"Database address"`
    Username      string        `name:"username" 
                                 group:"Database" 
                                 placeholder:"USERNAME" 
                                 help:"Database user"`
    Password      string        `name:"password" 
                                 group:"Database" 
                                 xor:"password" 
                                 placeholder:"PASSWORD" 
                                 help:"Database password"`
    Port          string        `name:"port" 
                                 group:"Database" 
                                 placeholder:"PORT" 
                                 default:${port} 
                                 help:"Database port (default: ${port})"`
    Socket        string        `name:"socket" 
                                 group:"Database" 
                                 placeholder:"SOCKET" 
                                 default:"${socket}" 
                                 help:"Database unix socket (default: ${socket})"`
    AskPass       bool          `name:"ask-pass" 
                                 group:"Database" 
                                 xor:"password" 
                                 help:"Prompt for a password"`
    DefaultsFile  string        `name:"defaults-file" 
                                 group:"Database" 
                                 placeholder:"FILE" 
                                 help:"Default database options file"`
    DefaultsGroup string        `name:"defaults-group" 
                                 group:"Database" 
                                 placeholder:"NAME" 
                                 default:"client" 
                                 help:"Defaults file section name"`
    TLS           bool          `name:"tls" 
                                 group:"Database" 
                                 help:"Use TLS connection to database"`
    Tasks         []string      `name:"tasks" 
                                 group:"Capture" 
                                 placeholder:"NAME" 
                                 enum:"${tasks}" 
                                 sep:"," 
                                 help:"Capture tasks to enable: ${tasks} (default: all)"`
    Interval      time.Duration `name:"interval" 
                                 group:"Capture" 
                                 default:"5s" 
                                 help:"Interval for collecting data"`
    Path          string        `name:"path" 
                                 group:"Capture" 
                                 placeholder:"PATH" 
                                 default:"${path}" 
                                 help:"Output directory (default: ${path})"`
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
            "path":   ".",
            "port":   "3306",
            "tasks":  strings.Join(lo.Map(allTasks, func(t captureTask, _ int) string { return t.name }), ","),
            "socket": "/var/run/mysqld/mysqld.sock",
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
        var err error
        config, err = loadConfigFile(config, options.DefaultsFile, options.DefaultsGroup)
        if err != nil {
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