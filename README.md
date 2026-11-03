# dankgo

Common Go modules used by DMS & the Dux Labs suite (dankcalendar, dgop, danksearch, and whatever comes next).

Every app in the suite was carrying its own copy of the same logger, unix-socket RPC, chi/huma scaffolding, and XDG path helpers. This is that code, extracted once.

## Packages

| Package | What it is |
|---|---|
| `log` | charmbracelet logger singleton. `log.SetEnvPrefix("DANKCAL")` → reads `DANKCAL_LOG_LEVEL` / `DANKCAL_LOG_FILE`. File sink strips ANSI. |
| `paths` | XDG dirs + per-process socket paths, keyed by app name. `paths.New("dankcal").SocketPath()` |
| `errdefs` | `CustomError`/`ErrorType` with HTTP status mapping. Apps register their own types from `AppErrorBase` up. stdlib-only. |
| `errdefs/humaerr` | huma error envelope + `HumaErrorFunc`. Split out so non-HTTP apps never pull huma. |
| `ipc` | JSON-over-unix-socket RPC: server (ping/subscribe/unsubscribe built in), client, event bus, mux. Stale sockets get reaped by PID. Apps with their own subscribe protocol set `SubscribeHandler`; per-connection handshake capabilities via `CapabilitiesFunc`. |
| `ipc/params` | Typed extraction from `map[string]any` request params. |
| `shellapp` | Quickshell app lifecycle: run/restart/kill commands, daemon supervision, PID/session tracking, config-dir resolution (`-c` → `<PREFIX>_SHELL_DIR` → live instance → embedded UI). App-specific behavior hooks in via `PreLaunch`, `ExtraEnv`, `OnUIExit`, and an optional `Done()` channel on the backend. |
| `shellapp/shellfs` | Materializes an embedded UI tree onto disk — content-hashed, read-only, pruned when stale. |
| `syncmap` | Generic `sync.Map` (typed keys and values). |
| `dbusutil` | godbus variant helpers: typed `Get`/`As`, recursive `Normalize`. |
| `httpapi` | huma/chi scaffold: `NewHumaConfig`, `/docs` page, router with health + recoverer, graceful `Server`. Use à la carte or via `NewRouter`. |
| `httpapi/middleware` | The chi-ported logger/recoverer/request-id set. |
| `netutil` | `GetIPAddress` header dance. |
| `lyrics` | Song lyrics lookup: sidecar files, disk cache, then LRCLIB / BetterLyrics / LyricsPlus raced in priority order. Typed line and word timing, voices, backing vocals. LRC, TTML and Lyricsfile parsers usable on their own. |
| `weather` | Weather forecast from Open-Meteo / MET Norway asked at once, first in priority order wins, the others fill its missing readings. Answers in the Open-Meteo shape; MET Norway is mapped, sunrise/sunset and day/night computed from the sun. `Command` runs external providers. |
| `app` | Thin bootstrap: `app.New(info, rootCmd)` wires logging + version, `app.Serve(ctx, runners...)` handles signals and shutdown. Not a framework. |
| `material` | Go port of Google's material-color-utilities: HCT, tonal palettes, the 2021 and 2025 dynamic color specs, Wu/WSMeans/Celebi quantizers. Apache-2.0, see `material/NOTICE`. |

## Using it

```
go get github.com/duxlabs-dev/dux-go@v0.1.0
```

## Minimal daemon

```go
a := app.New(app.Info{Name: "Dank Thing", ID: "dankthing", Version: version}, rootCmd)

srv := ipc.NewServer(ipc.Config{AppName: "dankthing", APIVersion: 1}, route)
if err := srv.Listen(); err != nil {
    return err
}
return app.Serve(ctx, srv)
```
