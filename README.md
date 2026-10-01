# AriNode

AriNode is a V2bX based node service with Xboard provisioning and Komari status integration. It retains the V2bX sing-box, Xray and Hysteria2 cores and uses Xboard's native UniProxy node API for configuration, users, traffic and alive reports. [中文说明](README.zh-CN.md).

This repository starts from [V2bX](https://github.com/wyx2685/V2bX) commit `71277de69efbbc86c23ad8ae02b68efd174e5756` under MPL-2.0. The Xboard adapter was checked against [Xboard-Node](https://github.com/cedar2025/Xboard-Node) and [Xboard](https://github.com/cedar2025/Xboard). The Komari plugin follows the [Komari plugin SDK](https://github.com/komari-monitor/plugin-sdk).

## One-command install (Linux)

On a Linux host with systemd, run:

```sh
curl -fsSL https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh | sudo bash
```

The installer downloads and verifies the latest stable release, then asks for Chinese or English and a deployment mode: fresh setup, migration from an existing xbnode, or keeping an AriNode config. If `/etc/xboard-node/config.yml` exists, migration is the default. Migration reads that config and its `credentials.env` without asking for the Xboard URL or token, backs up any existing AriNode config, and switches systemd services with a restart attempt for xbnode if AriNode fails to start. Fresh setup asks for the Xboard URL, token (hidden input), nodes such as `vless:1 trojan:2`, and optional machine ID. The config is mode `0600`. Automatic upgrades stay off until explicitly enabled. Linux `amd64` or `arm64`, `curl`, `tar`, `sha256sum`, and `systemctl` are required.

Run `sudo anctl bash` to reopen the language and deployment menu. Use `anctl status` and `anctl log` to check the service. For multiple Xboard or Komari panels, edit the [multi-binding config](example/multi-bindings.config.json) and run `sudo anctl restart`.

## Quick start

1. In Xboard, create and enable the nodes. Note each node's ID and the global server token under server settings.
2. Copy `example/arinode.config.json` to `config.json`. Set `ApiHost`, `ApiKey`, `NodeID` and `NodeType`. Add another object under `Nodes` for each additional node.
3. Run `docker compose up -d --build`. Host networking is used because proxy inbounds are created on the host's ports.
4. Check `docker compose logs -f arinode` and `curl http://127.0.0.1:18086/v1/status` on the host.

The image builds with Go 1.25. For a native build:

```sh
GOEXPERIMENT=jsonv2 go build -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' -o arinode .
GOEXPERIMENT=jsonv2 go build -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' -o anctl ./cmd/anctl
ARINODE_PANEL_TOKEN='YOUR_XBOARD_SERVER_TOKEN' ./anctl init --panel https://panel.example.com --node vless:1 --node trojan:2 --output config.json
./arinode server --config config.json
```

To run the native binary under systemd, run `sudo install -m 755 arinode anctl /usr/local/bin/`, copy the protected config to `/etc/arinode/config.json`, place [`deploy/arinode.service`](deploy/arinode.service) in `/etc/systemd/system/`, then run `systemctl daemon-reload && systemctl enable --now arinode`. Use `anctl start`, `anctl stop`, `anctl restart`, `anctl status`, and `anctl log` to manage that unit.

## Stable releases and upgrades

Official stable versions are named `YYYYMMDD-commit`, for example `20261001-abcdef12`. The date is the UTC date of the release commit and the suffix is its abbreviated Git hash. Development builds report `dev`. To publish a stable version, a maintainer can start **Test and release AriNode** from GitHub Actions with **Run workflow** on `main`, or push a matching tag for a commit already on `main`. The workflow tests and publishes Linux `amd64` and `arm64` archives containing both executables and a `SHA256SUMS` file. Only a published, non-prerelease GitHub Release with this version format is eligible for upgrades.

For a native Linux systemd installation:

```sh
anctl version
anctl upgrade check
sudo anctl upgrade
anctl upgrade auto status
sudo anctl upgrade auto enable
sudo anctl upgrade auto disable
```

Automatic upgrades are **disabled by default**. `auto enable` installs and starts a daily systemd timer; `auto disable` removes it. The timer checks the latest stable release and upgrades only when its version differs and is not older by date. `anctl upgrade` downloads the matching architecture, verifies GitHub's SHA256 asset digest, validates the two binaries and their embedded version, then replaces them and restarts an active `arinode.service`. A failed replacement or restart restores the previous binaries and attempts to restart the old service. Configuration files are not changed. Docker deployments should update the image through their normal deployment process; the systemd timer is for native Linux installations.

The `init` command creates a mode `0600` config file and refuses to overwrite it unless `--force` is supplied. It supports repeated `--node type:id` values. The token can also be passed with `--token`, but an environment variable avoids putting it in shell history. For Xboard's machine authentication, use the machine token and add `--machine-id ID`; the listed nodes must already be bound to that machine in Xboard.

## Migrate to or from Xboard-Node

On a Linux host with both systemd units installed, either direction is one command:

```sh
sudo anctl migrate from-xbnode --switch
sudo anctl migrate to-xbnode --switch
```

The defaults are `/etc/xboard-node/config.yml` and `/etc/arinode/config.json`. Import also reads `credentials.env` beside the xbnode config as literal key and value assignments to resolve token environment variables. Add `--force` if the destination exists; its previous contents are backed up to a timestamped `.bak-*` file. Use `--dry-run` to inspect the node count and warnings without writing a file or printing credentials. Omit `--switch` to convert only the config, or provide `--input` and `--output` for custom paths.

Import automatically queries Xboard for node types absent from the YAML and discovers current machine bindings. Offline single-node import can use `--offline --node-type vless`; machine import requires panel access. Exported machine bindings use Xboard-Node's dynamic discovery, which may include additional nodes bound to that machine. Some kernel customization, WebSocket, polling and runtime settings need manual review. Certificate paths are preserved, but certificate files are not copied. If the new systemd service fails to start, the command attempts to restart the previous service.

## Xboard plugin

Upload [`dist/arinode-xboard-plugin.zip`](dist/arinode-xboard-plugin.zip) in Xboard's plugin administration, then install and enable `ari_node`. Alternatively copy [`integrations/xboard/AriNode`](integrations/xboard/AriNode) to `<Xboard>/plugins/AriNode`. The plugin exposes an admin-only `POST /api/v1/arinode/provision` endpoint. Send `{"node_ids":[1,2],"core":"sing"}` using an authenticated Xboard admin session. The response is a complete AriNode `config.json` for those **existing enabled nodes**, including their IDs, types and Xboard server token. For machine authentication, create a machine in Xboard, bind the nodes to it, and add `"machine_id":3` to the request; the plugin verifies the bindings and returns that machine's token. Store the response as a secret (`chmod 600 config.json`). The endpoint does not create or change Xboard nodes.

Supported bindings: VMess, VLESS, Trojan, Shadowsocks, Hysteria2, TUIC and AnyTLS. A Hysteria node must have protocol version 2. Legacy UniProxy bindings use Xboard's global `server_token`; machine bindings use a machine-specific token.

## Komari plugin

Upload [`dist/arinode-komari-plugin.zip`](dist/arinode-komari-plugin.zip) through Komari's plugin manager. It adds an admin page and an admin-only `GET /api/arinode/status` route. Configure `status_url` if Komari runs in a different network namespace. By default the status listener binds to `127.0.0.1:18086`; for a remote listener set `ARINODE_STATUS_TOKEN` and configure the matching Komari `status_token`.

The status API returns the service start time, per-node states and Komari reporting states. It does not expose panel credentials. `/healthz` provides a simple local readiness check.

## Multiple Xboard and Komari bindings

Omitted `Cores`, core `Type`, or node `Core` defaults to sing-box. `sing`, `singbox`, and `sing-box` are accepted aliases for `sing`. An explicit `CoreName` selects its named core; other cores require explicit configuration and node selection. The status API includes each running node's active core and listening port. Panel reports alone do not verify client connectivity.

Use [`example/multi-bindings.config.json`](example/multi-bindings.config.json) to name multiple Xboard panels in `Panels` and reference each one from `Nodes`. Each binding can use its own server or machine token. Bindings on different panels with the same machine ID report machine status independently.

Add multiple `Komari` entries with a unique `Name`, panel `Endpoint`, and client `Token` or `TokenEnv`. AriNode reports host metrics independently to each Komari panel through Agent v2 HTTP RPC. This provides monitoring reports; Komari remote commands and WebSSH are outside this integration. The existing Komari plugin can be installed on each panel to view AriNode's local status API.

Failed Xboard nodes are retried every 30 seconds without stopping healthy nodes. Config reload only restarts changed bindings. `/v1/status` exposes each binding's `running` or `retrying` state and the Komari reporting state. Core configuration changes require `anctl restart`.

Isolation covers per-node configuration, panel calls, startup, reload errors, and background task panics. Bindings still share one AriNode process; a fatal crash of that process requires separate per-node processes for process-level isolation.

## TCP tuning

On a Linux host:

```sh
sudo bash scripts/tune.sh bbr
sudo bash scripts/tune.sh tcpfit
sudo bash scripts/tune.sh status
```

`bbr` checks kernel support, writes `/etc/sysctl.d/90-arinode-bbr.conf`, applies BBR + fq and verifies BBR. `tcpfit` downloads the pinned upstream `v0.3.8` release, verifies its SHA256 manifest, then starts the upstream installer/menu. These are host-level operations and should run on the host, not in the container. The [tcpfit project](https://github.com/Kylin010/tcpfit) owns its tuning logic.

## Current compatibility boundary

AriNode uses the same Xboard **UniProxy node-mode API** as Xboard-Node: `/api/v1/server/UniProxy/config`, `user`, `alivelist`, `push`, `alive` and `status`. For nodes assigned to a machine, it uses the corresponding `/api/v2/server/*` endpoints and reports host load to `/api/v2/server/machine/status`. Xboard-Node's automatic machine node discovery, WebSocket push, handshake and consolidated report API are not yet implemented in this V2bX fork. AriNode's machine bindings are explicitly listed in `config.json`. Komari client tokens must be created in their panels; AriNode does not create monitored clients automatically.

## License

MPL-2.0; see [LICENSE](LICENSE). Upstream attribution is retained in this repository.
