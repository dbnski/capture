package main

import (
    "context"
    "database/sql"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "path/filepath"
    "sync"
    "syscall"
    "time"

    "github.com/alecthomas/kong"
    "github.com/go-sql-driver/mysql"
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
    )

    slog.Info("Version", "version", Version, "build", Build, "commit", CommitHash)

    if options.Hostname == "" {
        slog.Error("--hostname is required")
        os.Exit(1)
    }

    config := &mysql.Config{
            User:                 options.Username,
            Passwd:               options.Password,
            AllowNativePasswords: true,
    }

    if options.Hostname == "" || options.Hostname == "localhost"{
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
            slog.Error("Failed to load defaults file", "file", options.DefaultsFile, "error", err)
            os.Exit(1)
        }
    }

    if options.AskPass {
        slog.Info("Password: ")
        passwd, err := term.ReadPassword(int(syscall.Stdin))
        if err != nil {
            slog.Error("Failed to read password", "error", err)
            os.Exit(1)
        }
        config.Passwd = string(passwd)
    }

    db, err := sql.Open("mysql", config.FormatDSN())
    if err != nil {
        slog.Error("Failed to open database connection", "error", err)
        os.Exit(1)
    }
    defer db.Close()

    db.SetConnMaxLifetime(24 * time.Hour)
    db.SetMaxOpenConns(3)

    if stat, err := os.Stat(options.Path); err != nil {
        slog.Error("Failed to use the output path", "path", options.Path, "error", err)
        os.Exit(1)
    } else if !stat.IsDir() {
        slog.Error("Output path is not a directory", "path", options.Path)
        os.Exit(1)
    } else {
        testFile := options.Path + "/.test"
        if f, err := os.Create(testFile); err != nil {
            slog.Error("Output path is not writable", "path", options.Path, "error", err)
            os.Exit(1)
        } else {
            f.Close()
            os.Remove(testFile)
        }
    }
    fullPath, _ := filepath.Abs(options.Path)
    slog.Info("Output path", "path", fullPath)

    ctx, cancel := context.WithCancel(context.Background())

    signals := make(chan os.Signal, 1)
    signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
    defer func() {
        signal.Stop(signals)
        cancel()
    }()

    go func() {
        select {
        case <- signals:
            cancel()
        case <- ctx.Done():
        }
    }()

    for {
        queryCtx, queryCancel := context.WithTimeout(ctx, 1 * time.Second)
        err := db.PingContext(queryCtx)
        queryCancel()

        if err == nil {
            slog.Info("Successfully connected to MySQL")
            break
        } else if ctx.Err() != nil {
            slog.Info("Shutting down")
            os.Exit(0)
        } else {
            slog.Error("Failed to connect to database, will retry", "error", err)
        }

        timer := time.NewTimer(1 * time.Second)
        select {
        case <- ctx.Done():
            timer.Stop()
            slog.Info("Shutting down")
            os.Exit(0)
        case <- timer.C:
            continue
        }
    }

    slog.Info("Capturing state information", "interval", options.Interval)

    var wg sync.WaitGroup
    var mu sync.Mutex

    wg.Add(3)
    go captureInnodbStatus(ctx, &wg, &mu, db)
    go captureProcesslist(ctx, &wg, &mu, db)
    go captureGlobalStatus(ctx, &wg, &mu, db)

    wg.Wait()

    slog.Info("Shutting down")
}