package main

import (
    "bufio"
    "bytes"
    "context"
    "database/sql"
    "database/sql/driver"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net"
    "strings"
    "time"
    "unsafe"

    "github.com/go-sql-driver/mysql"
)

type captureTask struct {
    name string
    fn   CaptureFunc
}

type CaptureFunc func(ctx context.Context, db *sql.DB, writer Writer) error

var allTasks = []captureTask{
    {"processlist", captureProcesslist()},
    {"innodb",      captureInnodb()},
    {"status",      captureStatus()},
}

func shouldRetry(err error) bool {
    if errors.Is(err, context.DeadlineExceeded) {
        return true
    }
    var netErr net.Error
    if errors.As(err, &netErr) {
        return true
    }

    if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
        return true
    }

    if errors.Is(err, driver.ErrBadConn) {
        return true
    }

    if errors.Is(err, mysql.ErrInvalidConn) {
        return true
    }

    var mysqlErr *mysql.MySQLError
    if errors.As(err, &mysqlErr) {
        if mysqlErr.Number == 1040 || mysqlErr.Number == 1053 || mysqlErr.Number == 1105 {
            return true
        }
        return false
    }

    return false
}

func capture(ctx context.Context, db *sql.DB, name string, fn CaptureFunc) error {
    writer := NewRotatingLogWriter(options.Path, name)
    defer writer.Close()

    timer := time.NewTimer(0)

    for {
        select {
        case <- ctx.Done():
            slog.Info("Capture stopped", "type", name)
            return nil
        case now := <- timer.C:
            timer.Reset(options.Interval)

            if err := writer.EnsureRotated(); err != nil {
                slog.Error("Failed to rotate log file", "type", name, "error", err)
                continue
            }

            fmt.Fprintf(writer, "---------+ TS %s --------------------------------------------------\n", now.Format(time.RFC3339))

            if err := fn(ctx, db, writer); err != nil {
                if errors.Is(err, context.Canceled) {
                    return nil
                }

                fmt.Fprintln(writer, "Capture error:", err.Error())

                if !shouldRetry(err) {
                    slog.Error("Fatal capture error", "type", name, "error", err)
                    return err
                }
                slog.Error("Capture error", "type", name, "error", err)
            }

            fmt.Fprintf(writer, "---------+ --------------------------------------------------------------------------\n\n")

            if err := writer.Flush(); err != nil {
                slog.Error("Failed to flush capture output", "type", name, "error", err)
            }
        }
    }
}

func captureInnodb() func (ctx context.Context, db *sql.DB, writer Writer) error {
    return func(ctx context.Context, db *sql.DB, writer Writer) error {
        var (
            engineType   string
            engineName   string
            engineStatus string
        )

        results, err := db.QueryContext(ctx, "SHOW ENGINE INNODB STATUS")
        if err != nil {
            return err
        }
        defer results.Close()

        now := time.Now()

        for results.Next() {
            err := results.Scan(&engineType, &engineName, &engineStatus)
            if err != nil {
                return err
            }

            reader  := strings.NewReader(engineStatus)
            scanner := bufio.NewScanner(reader)

            for scanner.Scan() {
                fmt.Fprintf(writer, "%s | %s\n", now.Format("15:04:05"), scanner.Text())
            }
        }

        return results.Err()
    }
}

func writeInSingleLineUnsafe(w io.Writer, s string) {
    space := []byte{' '}
    b := unsafe.Slice(unsafe.StringData(s), len(s))
    p := 0
    for {
        i := bytes.IndexByte(b[p:], '\n')
        if i == -1 {
            w.Write(b[p:])
            break
        }
        w.Write(b[p : p+i])
        w.Write(space)
        p += i + 1
    }
}

func captureProcesslist() func(ctx context.Context,db *sql.DB, writer Writer) error {
    return func(ctx context.Context, db *sql.DB, writer Writer) error {
        type ProcessList struct {
            Id           uint64
            User         sql.NullString
            Host         sql.NullString
            Db           sql.NullString
            Command      sql.NullString
            Time         sql.NullInt32
            State        sql.NullString
            Info         sql.NullString
            TimeMs       sql.NullInt64
            RowsSent     sql.NullInt64
            RowsExamined sql.NullInt64
        }

        var (
            pl        ProcessList
            count     int
            hasRows   bool
            hasTimeMs bool
        )

        results, err := db.QueryContext(ctx, "SHOW FULL PROCESSLIST")
        if err != nil {
            return  err
        }
        defer results.Close()

        columns, err := results.Columns()
        if err != nil {
            return err
        }
        record := make([]interface{}, len(columns))
        for i, col := range columns {
            switch strings.ToLower(col) {
            case "id":
                record[i] = &pl.Id
                count++
            case "user":
                record[i] = &pl.User
                count++
            case "host":
                record[i] = &pl.Host
                count++
            case "db":
                record[i] = &pl.Db
                count++
            case "command":
                record[i] = &pl.Command
                count++
            case "time":
                record[i] = &pl.Time
                count++
            case "state":
                record[i] = &pl.State
                count++
            case "info":
                record[i] = &pl.Info
                count++
            case "time_ms":
                record[i] = &pl.TimeMs
                hasTimeMs = true
            case "rows_sent":
                record[i] = &pl.RowsSent
                hasRows = true
            case "rows_examined":
                record[i] = &pl.RowsExamined
            default:
                record[i] = new(sql.RawBytes)
            }
        }

        if count != 8 {
            return errors.New("unexpected columns in processlist")
        }

        now := time.Now()

        for results.Next() {
            err := results.Scan(record...)
            if err != nil {
                return err
            }

            timestamp := now.Format("15:04:05")

            fmt.Fprintf(writer, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%-10s\t%d\t",
                timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
                pl.Command.String, pl.State.String, pl.Time.Int32)

            if hasTimeMs {
                fmt.Fprintf(writer, "%d\t", pl.TimeMs.Int64)
            }
            if hasRows {
                fmt.Fprintf(writer, "%d\t%d\t", pl.RowsSent.Int64, pl.RowsExamined.Int64)
            }

            writeInSingleLineUnsafe(writer, pl.Info.String)
            fmt.Fprintln(writer)
        }

        return results.Err()
    }
}

func captureStatus() func(ctx context.Context, db *sql.DB, writer Writer) error {
    return func(ctx context.Context, db *sql.DB, writer Writer) error {
        type Variable struct {
            Name  sql.NullString
            Value sql.NullString
        }

        record := []interface{} {
            new(sql.NullString),
            new(sql.NullString),
        }

        results, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS")
        if err != nil {
            return err
        }
        defer results.Close()

        now := time.Now()

        for results.Next() {
            err := results.Scan(record...)
            if err != nil {
                return err
            }

            v := &Variable{
                Name:  *record[0].(*sql.NullString),
                Value: *record[1].(*sql.NullString),
            }

            fmt.Fprintf(writer, "%s | %s = %s\n", now.Format("15:04:05"), v.Name.String, v.Value.String)
        }

        return results.Err()
    }
}