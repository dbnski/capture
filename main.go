package main

import (
    "bufio"
    "compress/gzip"
    "context"
    "errors"
    "path/filepath"
    "fmt"
    "io/fs"
    "os"
    "os/signal"
    "strings"
    "sync"
    "syscall"
    "time"
    "database/sql"
    "golang.org/x/term"

    "github.com/go-ini/ini"
    "github.com/alecthomas/kong"
    "github.com/go-sql-driver/mysql"
)

var options struct {
    Hostname      string        `name:"hostname" 
                                 placeholder:"ADDRESS" 
                                 default:"localhost" 
                                 help:"Database address"`
    Socket        string        `name:"socket" 
                                 placeholder:"PATH" 
                                 default:"/var/run/mysqld/mysqld.sock" 
                                 help:"Database unix socket (used when hostname is localhost)"`
    Port          string        `name:"port" 
                                 placeholder:"PORT" 
                                 default:"3306" 
                                 help:"Database port"`
    Username      string        `name:"username" 
                                 placeholder:"USERNAME" 
                                 help:"Database user"`
    Password      string        `name:"password" 
                                 xor:"password" 
                                 placeholder:"PASSWORD" 
                                 help:"Database password"`
    AskPass       bool          `name:"ask-pass" 
                                 xor:"password" 
                                 help:"Prompt for a password"`
    TLS           bool          `name:"tls" 
                                 help:"Use TLS connection to database"`
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

func loadConfigFile(c *mysql.Config, defaultsFile string, defaultsGroup string) error {
    options := ini.LoadOptions{
        AllowBooleanKeys: true,
        IgnoreContinuation: true,
        IgnoreInlineComment: true,
    }

    params, err := ini.LoadSources(options, defaultsFile)
    if err != nil {
        return err
    }

    for _, s := range params.Sections() {
        if s.Name() != defaultsGroup {
            continue
        }

        if c.User == "" && s.Key("user").String() != "" {
            c.User = s.Key("user").String()
        }
        if c.Passwd == "" && s.Key("password").String() != "" {
            c.Passwd = s.Key("password").String()
        }
        if c.Addr == "" && s.Key("socket").String() != "" {
            c.Net = "unix"
            c.Addr = s.Key("socket").String()
        }
        if c.Addr == "" && s.Key("hostname").String() != "" {
            c.Net = "tcp"
            c.Addr = s.Key("hostname").String()
        }
        if c.Addr == "" && s.Key("port").String() != "" {
            addr := strings.Split(c.Addr, ":")
            c.Addr = addr[0] + ":" + s.Key("port").String()
        }

        return nil
    }

    return fmt.Errorf("Client section not found")
}

func main() {
    kong.Parse(&options, kong.UsageOnError())

    config := &mysql.Config{
            User:                 options.Username,
            Passwd:               options.Password,
            AllowNativePasswords: true,
    }

    if options.Hostname == "" || options.Hostname == "localhost" || options.Hostname == "127.0.0.1" {
        config.Net  = "unix"
        config.Addr = options.Socket
    } else {
        config.Net  = "tcp"
        config.Addr = options.Hostname + ":" + options.Port
    }

    if options.TLS {
        config.Params = map[string]string{
            "tls": "skip-verify",
        }
        config.TLSConfig = "skip-verify"
    }

    if options.DefaultsFile != "" {
        if err := loadConfigFile(config, options.DefaultsFile, options.DefaultsGroup); err != nil {
            panic(err.Error())
        }
    }

    if options.AskPass {
        fmt.Print("Password: ")
        passwd, err := term.ReadPassword(int(syscall.Stdin))
        if err != nil {
            panic(err.Error())
        }
        config.Passwd = string(passwd)
        fmt.Println()
    }

    db, err := sql.Open("mysql", config.FormatDSN())
    if err != nil {
        panic(err.Error())
    }
    defer db.Close()

    db.SetConnMaxLifetime(60 * time.Second)
    db.SetMaxOpenConns(3)

    _, err = db.Query("SELECT VERSION()")
    if err != nil {
        fmt.Println(err.Error())
        os.Exit(255)
    }

    fmt.Printf("Successfully connected to MySQL.\n")
    fmt.Printf("Capturing state information at %s interval... (Press Ctrl+C to stop)\n", options.Interval)

    if _, err := os.Stat(options.Path); err != nil {
        if os.IsNotExist(err) {
            if err := os.Mkdir(options.Path, 0750); err != nil {
                panic(err.Error())
            }
        } else {
            panic(err.Error())
        }
    }

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

    var wg sync.WaitGroup
    var mu sync.Mutex

    wg.Add(3)

    go captureInnodbStatus(ctx, &wg, &mu, db)
    go captureProcesslist(ctx, &wg, &mu, db)
    go captureGlobalStatus(ctx, &wg, &mu, db)

    wg.Wait()

    fmt.Printf("Shutting down.\n")
}

type LogFile struct {
    f *os.File
    g *gzip.Writer
}

func OpenFile(name string, flag int, perm fs.FileMode) (*LogFile, error) {

    flag &= os.O_APPEND | os.O_CREATE | os.O_TRUNC | os.O_WRONLY

    if flag == 0 {
        return nil, errors.New("Invalid argument")
    }

    lf := &LogFile{}

    if f, err := os.OpenFile(name, flag, perm); err != nil {
        return nil, err
    } else {
        lf.f = f
    }

    lf.g = gzip.NewWriter(lf.f)

    return lf, nil
}

func (lf *LogFile) Close() (error) {
    if err := lf.g.Close(); err != nil {
        return err
    }

    return lf.f.Close()
}

func (lf *LogFile) Write(buffer []byte) (int, error) {
    return lf.g.Write(buffer)
}

func (lf *LogFile) Flush() (error) {
    return lf.g.Flush()
}

func captureInnodbStatus(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, db *sql.DB) {
    var (
        captureName string = "innodb-status"

        engineType   string
        engineName   string
        engineStatus string

        file *LogFile
        filename string
        timestamp time.Time
    )

    defer wg.Done()

    ticker := time.NewTicker(options.Interval)

    for {
        select {
        case <- ctx.Done():
            if file != nil {
                file.Close()
            }
            return
        case now := <- ticker.C:
            if timestamp == (time.Time{}) || timestamp.Hour() != now.Hour() {
                if filename != "" {
                    file.Close()
                }

                var err error

                mu.Lock()
                logPath := filepath.Join(options.Path, now.Format("20060102"))
                if _, err := os.Stat(logPath); err != nil {
                    if os.IsNotExist(err) {
                        if err := os.Mkdir(logPath, 0750); err != nil {
                            panic(err.Error())
                        }
                    } else {
                        panic(err.Error())
                    }
                }
                mu.Unlock()

                filename = filepath.Join(logPath, captureName + "." + now.Format("20060102T1500") + ".gz")
                file, err = OpenFile(filename, os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0640)
                if err != nil {
                    panic(err.Error())
                }
            }

            fmt.Fprintf(file, "---------+ TS %s ---------------------------------------------\n", now.Format(time.RFC3339))

            results, err := db.Query("SHOW ENGINE INNODB STATUS")
            if err != nil {
                fmt.Fprintln(file, err.Error())
                break
            }

            for results.Next() {
                err = results.Scan(&engineType, &engineName, &engineStatus)
                if err != nil {
                    panic(err.Error())
                }

                reader := strings.NewReader(engineStatus)
                scanner := bufio.NewScanner(reader)

                for scanner.Scan() {
                    fmt.Fprintf(file, "%s | %s\n", now.Format("15:04:05"), scanner.Text())
                }
            }

            fmt.Fprintf(file, "---------+ ---------------------------------------------------------------------\n\n")

            file.Flush()
        }
    }
}

func captureProcesslist(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, db *sql.DB) {
    defer wg.Done()

    type ProcessList struct {
        Id           uint64
        User         sql.NullString
        Host         sql.NullString
        Db           sql.NullString
        Command      sql.NullString
        Time         uint32
        State        sql.NullString
        Info         sql.NullString
        TimeMs       uint64
        RowsSent     uint64
        RowsExamined uint64
    }

    var (
        captureName string = "processlist"

        file *LogFile
        filename string
        timestamp time.Time
    )

    columns := func () ([]string) {
        results, err := db.Query("SHOW FULL PROCESSLIST")
        if err != nil {
            panic(err.Error())
        }

        var columns []string
        columns, err = results.Columns()
        if err != nil {
            panic(err.Error())
        }

        return columns
    }()

    record := make([]interface{}, len(columns))
    record[0] = new(uint64)         // Id
    record[1] = new(sql.NullString) // User
    record[2] = new(sql.NullString) // Host
    record[3] = new(sql.NullString) // Db
    record[4] = new(sql.NullString) // Command
    record[5] = new(uint32)         // Time
    record[6] = new(sql.NullString) // State
    record[7] = new(sql.NullString) // Info

    switch len(columns) {
    case 10:
        record[8] = new(uint64)     // RowsSent
        record[9] = new(uint64)     // RowsExamined
    case 11:
        record[8] = new(uint64)     // TimeMs
        record[9] = new(uint64)     // RowsSent
        record[10] = new(uint64)    // RowsExamined
    }

    ticker := time.NewTicker(options.Interval)

    for {
        select {
        case <- ctx.Done():
            if file != nil {
                file.Close()
            }

            return
        case now := <- ticker.C:
            if timestamp == (time.Time{}) || timestamp.Hour() != now.Hour() {
                if filename != "" {
                    file.Close()
                }

                var err error

                mu.Lock()
                logPath := filepath.Join(options.Path, now.Format("20060102"))
                if _, err := os.Stat(logPath); err != nil {
                    if os.IsNotExist(err) {
                        if err := os.Mkdir(logPath, 0750); err != nil {
                            panic(err.Error())
                        }
                    } else {
                        panic(err.Error())
                    }
                }
                mu.Unlock()

                filename = filepath.Join(logPath, captureName + "." + now.Format("20060102T1500") + ".gz")
                file, err = OpenFile(filename, os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0640)
                if err != nil {
                    panic(err.Error())
                }
            }

            fmt.Fprintf(file, "---------+ TS %s ---------------------------------------------\n", now.Format(time.RFC3339))

            results, err := db.Query("SHOW FULL PROCESSLIST")
            if err != nil {
                fmt.Fprintln(file, err.Error())
                break
            }

            for results.Next() {
                err = results.Scan(record...)
                if err != nil {
                    panic(err.Error())
                }

                pl := &ProcessList{
                    Id:      *record[0].(*uint64),
                    User:    *record[1].(*sql.NullString),
                    Host:    *record[2].(*sql.NullString),
                    Db:      *record[3].(*sql.NullString),
                    Command: *record[4].(*sql.NullString),
                    Time:    *record[5].(*uint32),
                    State:   *record[6].(*sql.NullString),
                    Info:    *record[7].(*sql.NullString),
                }

                switch len(record) {
                case 10:
                    pl.RowsSent     = *record[8].(*uint64)
                    pl.RowsExamined = *record[9].(*uint64)
                case 11:
                    pl.TimeMs       = *record[8].(*uint64)
                    pl.RowsSent     = *record[9].(*uint64)
                    pl.RowsExamined = *record[10].(*uint64)
                }

                info := strings.Replace(pl.Info.String, "\n", " ", -1)
                timestamp := now.Format("15:04:05")

                switch len(record) {
                case 8:
                    fmt.Fprintf(file, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%s\n",
                        timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
                        pl.Command.String, pl.Time, pl.State.String, info)
                case 10:
                    fmt.Fprintf(file, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%d\t%d\t%s\n",
                        timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
                        pl.Command.String, pl.Time, pl.State.String, pl.RowsSent, pl.RowsExamined, info)
                case 11:
                    fmt.Fprintf(file, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t%d\t\t%-10s\t%d\t%d\t%s\n",
                        timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
                        pl.Command.String, pl.Time, pl.TimeMs, pl.State.String, pl.RowsSent, pl.RowsExamined, info)
                }
            }

            fmt.Fprintf(file, "---------+ ---------------------------------------------------------------------\n\n")

            file.Flush()
        }
    }
}

func captureGlobalStatus(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, db *sql.DB) {
    defer wg.Done()

    type Variable struct {
        Name sql.NullString
        Value sql.NullString
    }

    var (
        captureName string = "global-status"

        file *LogFile
        filename string
        timestamp time.Time
    )

    record := []interface{} {
        new(sql.NullString),
        new(sql.NullString),
    }

    ticker := time.NewTicker(options.Interval)

    for {
        select {
        case <- ctx.Done():
            if file != nil {
                file.Close()
            }

            return
        case now := <- ticker.C:
            if timestamp == (time.Time{}) || timestamp.Hour() != now.Hour() {
                if filename != "" {
                    file.Close()
                }

                var err error

                mu.Lock()
                logPath := filepath.Join(options.Path, now.Format("20060102"))
                if _, err := os.Stat(logPath); err != nil {
                    if os.IsNotExist(err) {
                        if err := os.Mkdir(logPath, 0750); err != nil {
                            panic(err.Error())
                        }
                    } else {
                        panic(err.Error())
                    }
                }
                mu.Unlock()

                filename = filepath.Join(logPath, captureName + "." + now.Format("20060102T1500") + ".gz")
                file, err = OpenFile(filename, os.O_WRONLY | os.O_CREATE | os.O_APPEND, 0640)
                if err != nil {
                    panic(err.Error())
                }
            }

            fmt.Fprintf(file, "---------+ TS %s ---------------------------------------------\n", now.Format(time.RFC3339))

            results, err := db.Query("SHOW GLOBAL STATUS")
            if err != nil {
                fmt.Fprintln(file, err.Error())
                break
            }

            for results.Next() {
                err = results.Scan(record...)
                if err != nil {
                    panic(err.Error())
                }

                v := &Variable {
                    Name:  *record[0].(*sql.NullString),
                    Value: *record[1].(*sql.NullString),
                }

                fmt.Fprintf(file, "%s | %s = %s\n", now.Format("15:04:05"), v.Name.String, v.Value.String)
            }

            fmt.Fprintf(file, "---------+ ---------------------------------------------------------------------\n\n")

            file.Flush()
        }
    }
}