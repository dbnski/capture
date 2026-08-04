# MySQL Capture

A simple tool for capturing MySQL/MariaDB state information at regular intervals. Useful for troubleshooting performance issues or investigating incidents after the fact.

## What it does

Connects to your MySQL database and continuously captures:
- Process list
- InnoDB engine status
- RocksDB engine status (when the engine is available)
- Global status variables

Everything gets written to compressed, hourly-rotated log files organized by date.

## Database Permissions

Your database user needs the `PROCESS` privilege:
```sql
GRANT PROCESS ON *.* TO 'monitor'@'localhost';
```

`PROCESS` is required to see other users' threads in the process list and to run `SHOW ENGINE <engine> STATUS` commands.

## Building

```bash
make
```

Two environment variables can be overridden:

| Variable | Default | Description |
|---|---|---|
| `GOARCH` | host arch | Target architecture for cross-compilation (e.g. `amd64`, `arm64`) |
| `CGO_ENABLED` | `1` | Set to `0` to produce a fully static binary |

Example:
```bash
CGO_ENABLED=0 GOARCH=arm64 make
```

## Quick Start

Run:
```bash
# Connect and start capturing (default: 5 second intervals)
./capture --username monitor --ask-pass

# Specify output directory and interval
./capture --username monitor --password secret --path ./logs --interval 10s
```

Press Ctrl+C to stop.

## Connection Options

Connect via TCP:
```bash
./capture --hostname 192.168.1.100 --port 3306 --username monitor --password secret
```

Connect via Unix socket (default for localhost):
```bash
./capture --username monitor --ask-pass
```

Use a MySQL config file:
```bash
./capture --defaults-file ~/.my.cnf
```

## Output

Files are organized like this:
```
logs/
└── 20260216/
    ├── innodb.202602161500.gz
    ├── processlist.202602161500.gz
    └── status.202602161500.gz
```

A new file is started every hour, and the name holds the date and time of the hour
it covers.

Each file contains timestamped snapshots that you can analyze later:
```bash
zcat logs/20260216/processlist.*.gz | grep "SELECT"
```

## Retention

Old logs are kept until you delete them. Use `--retention` to delete them
automatically:
```bash
./capture --username monitor --ask-pass --path ./logs --retention 168h
```

The check runs at start and then once an hour. It deletes capture files that are
older than the given duration, and removes the date directory once it is empty. A
directory that holds other files is kept. The shortest retention is 1 hour.