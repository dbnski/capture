package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

func capture(ctx context.Context, mu *sync.Mutex, db *sql.DB, name string, capture func(ctx context.Context, db *sql.DB, writer *RotatingLogWriter) error) {
	writer := NewRotatingLogWriter(mu, name)
	defer writer.Close()

	timer := time.NewTimer(0)

	for {
		select {
		case <- ctx.Done():
			return
		case now := <- timer.C:
			timer.Reset(options.Interval)

			if err := writer.EnsureRotated(); err != nil {
				slog.Error("Failed to rotate log file", "type", name, "error", err)
				continue
			}

			fmt.Fprintf(writer, "---------+ TS %s ---------------------------------------------\n", now.Format(time.RFC3339))
			
			if err := capture(ctx, db, writer); err != nil {
				fmt.Fprintln(writer, "Capture error:", err.Error())
				slog.Error("Failed to capture data", "type", name, "error", err)
			}

			fmt.Fprintf(writer, "---------+ ---------------------------------------------------------------------\n\n")

			writer.Flush()
		}
	}
}

func captureInnodbStatus(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, db *sql.DB) {
	defer wg.Done()

	capture(ctx, mu, db, "innodb-status", func(ctx context.Context, db *sql.DB, writer *RotatingLogWriter) error {
		var (
			engineType   string
			engineName   string
			engineStatus string
		)

		results, err := db.Query("SHOW ENGINE INNODB STATUS")
		if err != nil {
			return err
		}

		now := time.Now()

		for results.Next() {
			err = results.Scan(&engineType, &engineName, &engineStatus)
			if err != nil {
				return err
			}

			reader := strings.NewReader(engineStatus)
			scanner := bufio.NewScanner(reader)

			for scanner.Scan() {
				fmt.Fprintf(writer, "%s | %s\n", now.Format("15:04:05"), scanner.Text())
			}
		}

		return nil
	})
}

func captureProcesslist(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, db *sql.DB) {
	defer wg.Done()

	capture(ctx, mu, db, "processlist", func(ctx context.Context, db *sql.DB, writer *RotatingLogWriter) error {
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

		results, err := db.Query("SHOW FULL PROCESSLIST")
		if err != nil {
			return  err
		}
		columns, err := results.Columns()
		if err != nil {
			return err
		}

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
		case 8:
		case 10:
			record[8] = new(uint64)     // RowsSent
			record[9] = new(uint64)     // RowsExamined
		case 11:
			record[8] = new(uint64)     // TimeMs
			record[9] = new(uint64)     // RowsSent
			record[10] = new(uint64)    // RowsExamined
		default:
			return errors.New("Unexpected number of columns in processlist")
		}

		now := time.Now()

		for results.Next() {
			err = results.Scan(record...)
			if err != nil {
				slog.Error("Failed to scan processlist", "error", err)
				break
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
				fmt.Fprintf(writer, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%s\n",
					timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
					pl.Command.String, pl.Time, pl.State.String, info)
			case 10:
				fmt.Fprintf(writer, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t\t%-10s\t%d\t%d\t%s\n",
					timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
					pl.Command.String, pl.Time, pl.State.String, pl.RowsSent, pl.RowsExamined, info)
			case 11:
				fmt.Fprintf(writer, "%s | %d\t%-12s\t%-32s\t%-12s\t%-10s\t%d\t%d\t\t%-10s\t%d\t%d\t%s\n",
					timestamp, pl.Id, pl.User.String, pl.Host.String, pl.Db.String,
					pl.Command.String, pl.Time, pl.TimeMs, pl.State.String, pl.RowsSent, pl.RowsExamined, info)
			}
		}

		return nil
	})
}

func captureGlobalStatus(ctx context.Context, wg *sync.WaitGroup, mu *sync.Mutex, db *sql.DB) {
	defer wg.Done()


	capture(ctx, mu, db, "global-status", func(ctx context.Context, db *sql.DB, writer *RotatingLogWriter) error {
		type Variable struct {
			Name  sql.NullString
			Value sql.NullString
		}

		record := []interface{} {
			new(sql.NullString),
			new(sql.NullString),
		}

		results, err := db.Query("SHOW GLOBAL STATUS")
		if err != nil {
			return err
		}

		now := time.Now()

		for results.Next() {
			err = results.Scan(record...)
			if err != nil {
				return err
			}

			v := &Variable{
				Name:  *record[0].(*sql.NullString),
				Value: *record[1].(*sql.NullString),
			}

			fmt.Fprintf(writer, "%s | %s = %s\n", now.Format("15:04:05"), v.Name.String, v.Value.String)
		}

		return nil
	})
}
