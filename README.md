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

### XBoard custom outbounds, routes and multiplex

AriNode applies panel `custom_outbounds`, native `custom_routes`, structured `custom_route_rules`, ordinary `routes` forwarding (`action: "proxy"`, `action_value: "sg01"`) and `multiplex` to the running Xray/sing-box kernels. Outbounds and route references are isolated per node, including proxy chains. Routing-only edits update automatically without restarting the inbound or shared Core; multiplex edits reload only the affected node. Local `OutboundConfigPath`, `RouteConfigPath`, `OriginalPath` and `MultiplexConfig` remain supported.

See the [complete HK01 → SG01 routing example, precedence, protocol matrix and verification guide](docs/xboard-routing.md). sing-box server mux applies enabled/padding/brutal; protocol and connection/stream counts are client/outbound settings. Xray server mux needs no equivalent inbound fields. The pinned sing-box build has no Naive outbound and rejects it explicitly; Mieru custom outbounds use the existing SDK.

Xboard `block` matches accept plain domains and `*.example.com` (the domain and its subdomains), IP/CIDR, and the `domain:`, `full:`, `keyword:`, `regexp:` and `protocol:` prefixes. Plain domains match a domain suffix; regexps require `regexp:`. Rule updates are validated before replacing active rules. If an older version reports `invalid domain rule` with a retrying node and no proxy listener, run `sudo anctl upgrade`.

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

Supported bindings: VMess, VLESS, Trojan, Shadowsocks, Hysteria 1/2, TUIC v5, AnyTLS and Mieru. Xboard's native `hysteria` binding with `version: 2` uses Hysteria2 internally. Xray bindings support VMess, VLESS, Trojan and Shadowsocks; use `sing` for the others. Legacy UniProxy bindings use Xboard's global `server_token`; machine bindings use a machine-specific token. See the [protocol verification matrix](docs/protocol-compatibility.md).

## Komari plugin

Upload [`dist/arinode-komari-plugin.zip`](dist/arinode-komari-plugin.zip) through Komari's plugin manager. It adds an admin page and an admin-only `GET /api/arinode/status` route. Configure `status_url` if Komari runs in a different network namespace. By default the status listener binds to `127.0.0.1:18086`; for a remote listener set `ARINODE_STATUS_TOKEN` and configure the matching Komari `status_token`.

The status API returns the service start time, per-node states and Komari reporting states. It does not expose panel credentials. `/healthz` provides a simple local readiness check.

## Multiple servers sharing one node

Run the same Xboard node ID on multiple AriNode servers and point its hostname at multiple A/AAAA records. Add `Cluster: {"Domain":"pool.example.com","MemberID":"hk-01"}` to that node on each server, using a different member ID per host. Update and enable **Xboard plugin 0.2.0 first** to merge traffic, devices and host metrics, and generate individual replica configs. See the [deployment guide](docs/node-cluster.md) and [example](example/node-cluster.config.json). DNS and clients select a backend; distribution is not CPU weighted and failed IPs are not removed automatically. Device counts are eventually synchronized, and speed limits apply per server.

## Multiple Xboard and Komari bindings

Omitted `Cores`, core `Type`, or node `Core` defaults to sing-box. `sing`, `singbox`, and `sing-box` are accepted aliases for `sing`. An explicit `CoreName` selects its named core; other cores require explicit configuration and node selection. The status API includes each running node's active core and listening port. Panel reports alone do not verify client connectivity.

**xhttp / splithttp exception:** the bundled sing-box does not support these transports. A sing-box binding without `CoreName` automatically uses bundled Xray for them, reusing a configured Xray core or starting one on demand. No separate binary is needed. Other transports retain the requested core, including after panel transport changes. A named sing-box binding reports that Xray is required. Native Xboard/PHP empty object arrays in xhttp `extra` are normalized before parsing. Upgrade if an older version logs `unknown transport type: xhttp`.

**Client settings:** server-side normalization does not rewrite client subscriptions. For client `cannot unmarshal JSON array` errors, change empty object fields from `[]` to `{}` or omit them. Remove `extra.downloadSettings` unless a separate download endpoint is intentionally configured; a null address, unrelated port, or mismatched TLS/Reality setting is not a working same-node default. Use the [single-node xhttp extra example](example/xhttp-extra.json) in the panel, retain the node's host/path/Reality settings, and refresh the client subscription.

**Mieru:** use `NodeType: "mieru"` with the `sing` core (or omit `Core`). AriNode embeds [Mieru v3.29.0](https://github.com/enfein/mieru/tree/v3.29.0), supports the panel's TCP/UDP transport and `traffic_pattern`, and uses each subscriber UUID as both username and password. The inbound shares sing-box routing, limits and traffic reporting. Open the node port for the selected transport.

**Expiration and revocation:** authorization follows the eligible users returned by Xboard's `/user` endpoint; the native API does not provide subscription expiration timestamps. Polling uses the panel's `pull_interval`, capped at 30 seconds, plus panel caching, response and network delays. Successful revocations (including an empty user list) close the affected Mieru/AnyTLS user's existing TCP/UDP sessions and reject fresh connections and new streams on pooled transports while preserving healthy users. Revocations are applied before node configuration requests/reloads, and failed reloads cannot restore removed users. Failed core updates invalidate the user ETag so a full snapshot is retried. If the panel is unavailable, the last valid authorization remains in effect.

**Machine discovery:** machine bindings automatically fetch `/api/v2/server/machine/nodes` on startup and every 30 seconds. Nodes added in Xboard start automatically; unbound nodes stop after a valid response. Unchanged nodes retain their connections, and failed or malformed discovery responses preserve existing bindings. Explicit local entries provide per-node options; new entries inherit the machine's first local binding's options and receive a unique runtime tag. Set `MachineAutoDiscover: false` on every local entry for that machine to manage only explicit `Nodes` entries. Runtime discovery does not rewrite `config.json`.

**TLS:** omitted `CertConfig` uses `CertMode: "auto"`. For a TLS node whose panel explicitly sets `tls_settings.allow_insecure: true`, auto mode creates an in-memory self-signed certificate. A verified client requires explicit `file`, `http`, `dns`, or `self` certificate configuration; auto mode reports a configuration error when verification is required. Explicit `none` still disables TLS and cannot run QUIC/TLS-only protocols. Existing explicit certificate settings are retained. TUIC uses the panel's ALPN list, or `h3` when omitted. Native PHP empty network objects/headers and numeric/string Reality fields are accepted.

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

AriNode uses the same Xboard **UniProxy node-mode API** as Xboard-Node: `/api/v1/server/UniProxy/config`, `user`, `alivelist`, `push`, `alive` and `status`. For nodes assigned to a machine, it uses the corresponding `/api/v2/server/*` endpoints, discovers machine bindings, and reports host load to `/api/v2/server/machine/status`. Xboard-Node's WebSocket push, handshake and consolidated report API are not yet implemented in this V2bX fork. Komari client tokens must be created in their panels; AriNode does not create monitored clients automatically.

## License

MPL-2.0; see [LICENSE](LICENSE). Upstream attribution is retained in this repository.
