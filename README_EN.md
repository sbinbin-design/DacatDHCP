<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="web/dhcp-white.svg">
    <img src="web/dhcp.ico" width="120" alt="DacatDHCP">
  </picture>
</p>

<h1 align="center">DacatDHCP</h1>

> English | [中文](README.md)

DacatDHCP is a portable lightweight DHCP service tool for Windows. It packages the DHCP service and a graphical management interface into a single EXE — no installation, no extra runtime dependencies. Just double-click to run. It is ideal for quickly providing IP address allocation in self-hosted, test, or isolated networks.

## Core Features

- **Single-EXE deployment**: The frontend UI, icons, and version resources are embedded into the binary via `go:embed`. Release requires only `DacatDHCP.exe`.
- **Graphical management**: Manage via a browser at `http://127.0.0.1:8765` — adapter selection, address pool configuration, lease viewing, and runtime logs.
- **Adapter binding**: The DHCP service binds only to the manually selected adapter and does not affect other adapters.
- **Address pool management**: Automatically excludes the network address, broadcast address, and server IP. Supports up to 4096 addresses with one-click recommended pool autofill.
- **Optional gateway and DNS**: Both gateway and DNS are optional — leave empty to skip the corresponding Option. The gateway must not fall within the address pool range.
- **Lease lifecycle**: DISCOVER creates a 60-second pending Offer; REQUEST converts it to an active lease after validation. REQUEST ACKs only if the requested IP is available, otherwise NAKs.
- **Dark and light themes**: Supports Light / Dark themes. Click the theme icon button at the top to toggle. The choice is saved locally and persists across refreshes and restarts.
- **Chinese/English i18n**: The UI supports Chinese and English. The language choice is saved locally; the first launch auto-selects based on the system language.
- **System tray**: Minimizes to the tray. Double-click to reopen the console; right-click for status and exit.
- **Single instance**: Re-launching opens the existing console and exits the new process.
- **Safe shutdown**: Handles Windows shutdown, logoff, and session-end events to ensure DHCP port, HTTP port, and background goroutines are released.

## Supported Systems

Officially supported:

- Windows 10 / Windows 11
- Windows Server 2016 and later

Administrator privileges are required (the DHCP service binds to port 67). The program automatically requests elevation on startup.

## How to Run

1. Run `DacatDHCP.exe` as an administrator (the program auto-elevates).
2. On startup, the browser automatically opens the management console at `http://127.0.0.1:8765`.
3. In the console, select the service adapter, configure the address pool, and click "Start DHCP".
4. When minimized, the program stays in the system tray. Double-click the tray icon to reopen the console.

If port 8765 is occupied, the program shows an error and exits — it will not leave a background process without a UI.

## Configuration

The configuration file is located at `data/config.json` next to the executable and is auto-created on first run. Configurable fields:

| Field | Description |
| --- | --- |
| `adapter_name` | Adapter the service binds to |
| `pool_start` | Address pool start IP |
| `pool_end` | Address pool end IP |
| `lease_minutes` | Lease time in minutes |
| `web_port` | Management page port (default 8765) |
| `gateway` | Default gateway (optional; leave empty to skip Option 3) |
| `dns_servers` | DNS server list (optional; up to 3; leave empty to skip Option 6) |

Runtime logs are written to `data/dhcpsrv.log`. The `data` directory and config file are not included in the source or release packages — they are generated at runtime.

## Starting and Stopping DHCP

- **Start**: In the console, select an adapter and fill in the address pool, then click "Start DHCP". Configuration inputs are locked while running.
- **Stop**: Click "Stop Service", or exit via the tray menu.
- On adapter anomalies (e.g. disconnection), the service auto-stops and updates the tray status.

## Clearing Logs

- The log card in the console provides a "Clear Logs" button. Click it to open a confirmation dialog.
- After confirmation, a `POST /api/logs/clear` request is sent. The backend `Logger.Clear()` runs under a mutex with the following flow:
  - It first tries `Truncate(0)` on the existing file handle. On success it clears the in-memory ring buffer; the original handle remains unchanged.
  - If `Truncate(0)` fails (Windows `O_APPEND` handles do not support truncation), it falls back to reopening the file with `O_TRUNC`: a new handle is opened first, and only after that succeeds is the old handle closed and replaced, then the in-memory ring buffer is cleared.
  - If reopening also fails, the original file handle and in-memory logs are preserved and an error is returned, so subsequent logging continues without interruption.
- On success, the log list is refreshed from the backend immediately.
- Only clearing the frontend DOM without notifying the backend is prohibited, to avoid old logs being restored on the next poll.

## Theme and Language Switching

- **Theme**: Click the theme icon button at the top (moon icon in light mode, sun icon in dark mode) to toggle between Light / Dark. The choice is saved in the browser's local storage and persists across refreshes and restarts. Defaults to Light on first launch.
- **Language**: Click the "中文｜EN" segmented control at the top to switch language; the current language is highlighted. You can also choose in "Settings". The first launch auto-selects based on the system language (defaults to Chinese).

## Security Warning

> This tool is only for self-hosted, test, or isolated networks. **Do not enable it on production networks that already have a DHCP service.** Having multiple DHCP services on the same network causes IP allocation conflicts and network failures.

DacatDHCP does not automatically enable Windows routing, NAT, or Internet sharing. Filling in a gateway only means delivering that address to clients.

## Build

### Requirements

- Go 1.26.4
- Windows (amd64)
- Build scripts are in the `scripts/` directory

### Steps

1. Generate Windows PE resources (icon and version info):
   ```
   scripts\generate_resource.bat
   ```
2. Build the single EXE (includes gofmt, go vet, and go test checks):
   ```
   scripts\build.bat
   ```
3. The output is `dist\DacatDHCP.exe`.

The build script validates that the Go version is exactly 1.26.4 and verifies the EXE's version resource matches `internal/version/versioninfo.json`.

### Testing

- Unit tests: `go test ./...`
- Race detection: `scripts\test_race.bat` (requires a C compiler, CGO_ENABLED=1)

## Single-EXE Release Notes

The application itself is a single EXE — running requires only `DacatDHCP.exe`:

- All frontend HTML/CSS/JS, icons, language resources, and theme scripts are embedded into the binary via `go:embed`.
- No external CDN, online fonts, or runtime network dependencies.
- Built with `-ldflags="-H=windowsgui"` as a Windows GUI subsystem binary — no visible CMD window.
- Version info (product name, version, copyright) is written into the PE resource via `goversioninfo` and is visible in file properties.

The formal release archive (`dist/DacatDHCP-v<version>-windows-amd64.zip`) must include the following files:

- `DacatDHCP.exe` — main program
- `LICENSE` — Apache License 2.0 full text
- `NOTICE` — copyright and attribution notice
- `TRADEMARKS.md` — trademark notice

`scripts/build.bat` automatically generates this ZIP archive after building.

## Directory Structure

```
DacatDHCP/
├── cmd/dacatdhcp/          # Entry point main.go
├── internal/
│   ├── dhcp/               # DHCP core protocol and service
│   ├── network/            # Adapter enumeration and lookup
│   ├── server/             # HTTP management API and config
│   ├── singleinstance/     # Single-instance detection
│   ├── systray/            # System tray
│   └── version/            # Version info (single source: versioninfo.json)
├── web/                    # Frontend assets (embedded via go:embed)
│   ├── index.html          # Management page
│   ├── style.css           # Styles (light/dark themes and responsive)
│   ├── app.js              # Business logic
│   ├── i18n.js             # Chinese/English language resources
│   ├── theme.js            # Theme management
│   ├── embed.go            # embed declaration
│   └── dhcp.ico            # Icon
├── scripts/                # Build and test scripts
└── internal/version/versioninfo.json  # Single source of version info
```

## FAQ

**Q: Why are administrator privileges required?**
A: The DHCP service binds to UDP port 67 (a privileged port below 1024), which requires administrator privileges.

**Q: The browser doesn't open automatically after startup?**
A: Manually visit `http://127.0.0.1:8765`. If the port is occupied, the program shows an error and exits.

**Q: The recommended address pool is empty or says insufficient subnet space?**
A: The selected adapter's subnet has too few usable addresses. Switch adapters or specify the address pool manually.

**Q: Why can't the gateway be inside the address pool?**
A: An overlapping gateway and pool cause client configuration conflicts. The program refuses to start and prompts you to adjust.

**Q: Does the log content get translated when switching languages?**
A: No. DHCP raw logs remain as-is; only UI labels and program-generated prompts are translated.

## License

DacatDHCP is licensed under the Apache License 2.0.

Copyright 2026 DACAT.CC.

Redistributions must retain the applicable license, copyright, and attribution notices. See [LICENSE](LICENSE), [LICENSE.zh-CN.md](LICENSE.zh-CN.md), [NOTICE](NOTICE), and [TRADEMARKS.md](TRADEMARKS.md) for details.
