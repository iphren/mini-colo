# mini-colo

Collects power, temperature and network usage for a small box, serves a live
dashboard, and writes a monthly usage report. One Go binary, one SQLite file.

## Usage

Copy `.env.example` to `.env` and fill it in, then:

```sh
make build
./mini-colo collect            # take one sample and print it
./mini-colo serve              # collect every hour, dashboard on :8080
./mini-colo report             # write reports/<last month>.html
./mini-colo report -month 2026-09 -out some/dir
```

The dashboard also serves reports at `/report?month=2026-09`.

Reports cover calendar months by default. To match a billing cycle instead,
set `REPORT_START_DAY` in `.env`: with `15`, `-month 2026-09` covers
15 September to 14 October. Change the day in `deploy/mini-colo-report.timer`
to match.

## Deploying

```sh
make linux                     # or: make linux GOARCH=arm64
scp dist/mini-colo-linux-amd64 box:/opt/mini-colo/mini-colo
scp .env box:/opt/mini-colo/
```

On the box, set `User=` in the files under `deploy/`, copy them to
`/etc/systemd/system/`, and run:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now mini-colo.service mini-colo-report.timer
```

The box needs `ssh` and nothing else.

## How it works

The power meter and vnstat both report cumulative counters. Raw samples are
stored as they are, and usage is always computed as the difference between
consecutive samples, treating a counter that goes backwards as a reset.

## Reading the code

| File | What it does |
|---|---|
| `main.go` | Subcommand dispatch and signal handling |
| `config.go` | `.env` loading and settings |
| `collect.go` | One sample: HTTP to the power meter, `ssh` to the router |
| `store.go` | SQLite storage |
| `usage.go` | Counter deltas and time buckets (the core logic; see `usage_test.go`) |
| `server.go` | Collector loop, dashboard API, embedded static files |
| `report.go`, `templates/report.html` | Monthly report |
| `web/` | Dashboard page; `chart.js` is shared with the report |
