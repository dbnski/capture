# MySQL Capture

A simple tool for capturing MySQL/MariaDB state information at regular intervals. Useful for troubleshooting performance issues or investigating incidents after the fact.

## What it does

Connects to your MySQL database and continuously captures three things:
- Process list
- InnoDB engine status
- Global status variables

Everything gets written to compressed, hourly-rotated log files organized by date.

## Database Permissions

Your database user needs the `PROCESS` and `SUPER` privileges:
```sql
GRANT PROCESS, SUPER ON *.* TO 'monitor'@'localhost';
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
    ├── innodb-status.20260216T1500.gz
    ├── processlist.20260216T1500.gz
    └── global-status.20260216T1500.gz
```

Each file contains timestamped snapshots that you can analyze later:
```bash
zcat logs/20260216/processlist.*.gz | grep "SELECT"
```