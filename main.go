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

    "github.com/go-ini/ini"
    "github.com/alecthomas/kong"
    "github.com/go-sql-driver/mysql"
)

var options struct {
    Hostname     string        `name:"hostname"      placeholder:"ADDRESS"                      help:"Server address" required:""`
    Port         string        `name:"port"          placeholder:"PORT"     default:"3306"      help:"Server port"`
    Username     string        `name:"username"      placeholder:"USERNAME"                     help:"Database user"`
    Password     string        `name:"password"      placeholder:"PASSWORD"                     help:"Database password"`
    DefaultsFile string        `name:"defaults-file" placeholder:"FILE"                         help:"Read database options from the given file"`
    Path         string        `name:"path"          placeholder:"PATH"     default:"."         help:"Logging directory"`
    Interval     time.Duration `name:"interval"      placeholder:"DURATION" default:"5s"        help:"Duration between collecting data samples"`
}

func getDSNFromConfig(config *mysql.Config) (string) {
    return config.FormatDSN()
}

func getConfigFromDefaultsFile(defaults_file string, config *mysql.Config) (error) {

    params, err := ini.LoadSources(ini.LoadOptions{AllowBooleanKeys: true}, defaults_file)
    if err != nil {
        return err
    }

    for _, s := range params.Sections() {
        if s.Name() != "client" {
                continue
        }

        if config.User == "" && s.Key("user").String() != "" {
            config.User = s.Key("user").String()
        }
        if config.Passwd == "" && s.Key("password").String() != "" {
            config.Passwd = s.Key("password").String()
        }
        if config.Addr == "" && s.Key("hostname").String() != "" {
            config.Net = "tcp"
            config.Addr = s.Key("hostname").String()
        }
        if config.Addr == "" && s.Key("port").String() != "" {
            addr := strings.Split(config.Addr, ":")
            config.Addr = addr[0] + ":" + s.Key("port").String()
        }

        return nil
    }

    return fmt.Errorf("Client section not found")
}

func main() {
    kong.Parse(&options, kong.UsageOnError())

    config := &mysql.Config {
        Addr:                 options.Hostname + ":" + options.Port,
        Net:                  "tcp",
        User:                 options.Username,
        Passwd:               options.Password,
        AllowNativePasswords: true,
    }

    if options.DefaultsFile != "" {
        if err := getConfigFromDefaultsFile(options.DefaultsFile, config); err != nil {
            panic(err.Error())
        }
    }

    dsn := getDSNFromConfig(config)

    db, err := sql.Open("mysql", dsn)
    if err != nil {
        panic(err.Error())
    }
    defer db.Close()

    db.SetConnMaxLifetime(60 * time.Second)
    db.SetMaxOpenConns(3)

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

        <-signals
        os.Exit(-1)
    }()

    var wg sync.WaitGroup

    wg.Add(2)

    go capture_innodb_status(ctx, &wg, db)
    go capture_processlist(ctx, &wg, db)

    wg.Wait()
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

func capture_innodb_status(ctx context.Context, wg *sync.WaitGroup, db *sql.DB) {
    var (
        CaptureName string = "innodb-status"
        EngineType string
        EngineName string
        EngineStatus string

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

                datepath := filepath.Join(options.Path, now.Format("20060102"))
                if _, err := os.Stat(datepath); err != nil {
                    if os.IsNotExist(err) {
                        if err := os.Mkdir(datepath, 0750); err != nil {
                            panic(err.Error())
                        }
                    } else {
                        panic(err.Error())
                    }
                }

                filename = filepath.Join(datepath, CaptureName + "." + now.Format("20060102T1500") + ".gz")
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
                err = results.Scan(&EngineType, &EngineName, &EngineStatus)
                if err != nil {
                    panic(err.Error())
                }

                reader := strings.NewReader(EngineStatus)
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

type ProcessList struct {
    Id uint64
    User sql.NullString
    Host sql.NullString
    Db sql.NullString
    Command sql.NullString
    Time uint32
    State sql.NullString
    Info sql.NullString
    RowsSent uint64
    RowsExamined uint64
}

func capture_processlist(ctx context.Context, wg *sync.WaitGroup, db *sql.DB) {
    var (
        CaptureName string = "processlist"

        file *LogFile
        filename string
        timestamp time.Time
    )

    defer wg.Done()

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

    record := []interface{} {
        new(uint64),
        new(sql.NullString),
        new(sql.NullString),
        new(sql.NullString),
        new(sql.NullString),
        new(uint32),
        new(sql.NullString),
        new(sql.NullString),
    }

    if len(columns) == 10 {
        record = []interface{} {
            new(uint64),
            new(sql.NullString),
            new(sql.NullString),
            new(sql.NullString),
            new(sql.NullString),
            new(uint32),
            new(sql.NullString),
            new(sql.NullString),
            new(uint64),
            new(uint64),
        }
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

                datepath := filepath.Join(options.Path, now.Format("20060102"))
                if _, err := os.Stat(datepath); err != nil {
                    if os.IsNotExist(err) {
                        if err := os.Mkdir(datepath, 0750); err != nil {
                            panic(err.Error())
                        }
                    } else {
                        panic(err.Error())
                    }
                }

                filename = filepath.Join(datepath, CaptureName + "." + now.Format("20060102T1500") + ".gz")
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

                if len(record) == 8 {
                    pl := &ProcessList {
                        Id: *record[0].(*uint64),
                        User: *record[1].(*sql.NullString),
                        Host: *record[2].(*sql.NullString),
                        Db: *record[3].(*sql.NullString),
                        Command: *record[4].(*sql.NullString),
                        Time: *record[5].(*uint32),
                        State: *record[6].(*sql.NullString),
                        Info: *record[7].(*sql.NullString),
                    }

                    fmt.Fprintf(file, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%s\n", now.Format("15:04:05"), 
                                    pl.Id, pl.User.String, pl.Host.String, pl.Db.String, pl.Command.String, pl.Time, pl.State.String,
                                    strings.Replace(pl.Info.String, "\n", " ", -1))
                } else if len(record) == 10 {
                    pl := &ProcessList {
                        Id: *record[0].(*uint64),
                        User: *record[1].(*sql.NullString),
                        Host: *record[2].(*sql.NullString),
                        Db: *record[3].(*sql.NullString),
                        Command: *record[4].(*sql.NullString),
                        Time: *record[5].(*uint32),
                        State: *record[6].(*sql.NullString),
                        Info: *record[7].(*sql.NullString),
                        RowsSent: *record[8].(*uint64),
                        RowsExamined: *record[9].(*uint64),
                    }

                    fmt.Fprintf(file, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%d\t%d\t%s\n", now.Format("15:04:05"), 
                                    pl.Id, pl.User.String, pl.Host.String, pl.Db.String, pl.Command.String, pl.Time, pl.State.String,
                                    pl.RowsSent, pl.RowsExamined, strings.Replace(pl.Info.String, "\n", " ", -1))
                }
            }

            fmt.Fprintf(file, "---------+ ---------------------------------------------------------------------\n\n")

            file.Flush()
        }
    }
}