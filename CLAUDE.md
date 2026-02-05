# Cloudflared Quick Tunnel - Stripped Fork

## Project Goal
Strip cloudflared to bare minimum for quick tunnel functionality only (trycloudflare.com).

## Build Command
```bash
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o cloudflared ./cmd/cloudflared
```

## Test Command
```bash
./cloudflared tunnel --url http://localhost:8080
```

## What Was Removed

### Deleted Directories
- `metrics/` - Prometheus metrics (3 files)
- `fips/` - FIPS compliance (2 files)
- `hello/` - Hello world demo server

### Deleted Files
- `tlsconfig/hello_ca.go` - Hello world TLS certificates

### Removed Features
- **Prometheus/metrics**: Replaced with no-op implementations in `*_metrics.go` files
- **FIPS mode**: Hardcoded as disabled in `supervisor/tunnel.go`
- **Hello world server**: `--hello-world` flag and demo functionality
- **Config file support**: Removed YAML config file reading, only CLI flags
- **File logging**: Removed --logfile and --log-directory, logs go to stderr only
- **Unix socket support**: Removed `--unix-socket` flag and validation
- **Bastion mode**: Removed `--bastion` flag
- **Trace output**: `--trace-output` flag
- **Pidfile**: `--pidfile` flag
- **Stdin control**: `--stdin-control` flag
- **Secret flags handling**: Simplified (no token support needed)
- **Many unused flags**: Replaced with hardcoded defaults

### Hardcoded Defaults (tunnel/configuration.go)
- `HAConnections: 1` (quick tunnels use single connection)
- `Retries: 5`
- `MaxEdgeAddrRetries: 8`
- `RPCTimeout: 5 * time.Second`
- `EdgeIPVersion: allregions.Auto`
- `gracePeriod: 30 * time.Second`

### Simplified Flags
Only CLI flags that work:
- `--url` - Local service URL to tunnel (required)
- `--loglevel` - Log level (debug, info, warn, error, fatal)

## Key Files Modified
- `cmd/cloudflared/tunnel/cmd.go` - Main tunnel command
- `cmd/cloudflared/tunnel/configuration.go` - Tunnel config with hardcoded values
- `cmd/cloudflared/tunnel/subcommands.go` - Removed unused flag definitions
- `cmd/cloudflared/flags/flags.go` - Reduced to minimal constants
- `cmd/cloudflared/cliutil/handler.go` - Removed config file reading
- `cmd/cloudflared/cliutil/logger.go` - Simplified logging flags
- `config/configuration.go` - Removed config file reading and unused validators
- `logger/configuration.go` - Console-only logging config
- `logger/create.go` - Removed file/rolling log support
- `supervisor/tunnel.go` - Hardcoded FIPS as disabled
- `ingress/origin_service.go` - Removed helloWorld struct
- `ingress/ingress.go` - Simplified to only support --url
- `ingress/config.go` - Removed bastion flag handling
- `tlsconfig/certreloader.go` - Removed hello certificate

## Binary Size Progress
- Original: ~21MB
- After prometheus removal: ~19MB
- After hello world removal: 19MB
- After config/logging simplification: 18MB
- After unused flags cleanup: 18MB

## Working Features
- Quick tunnels via `--url` flag
- QUIC transport protocol
- Post-quantum cryptography (X25519MLKEM768)
- ICMP proxy
- Console logging (stderr) with level and JSON format options
