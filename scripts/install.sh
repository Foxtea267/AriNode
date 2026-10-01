#!/usr/bin/env bash
set -euo pipefail

repo='Foxtea267/ariNode'
release_base="https://github.com/${repo}/releases/latest/download"
config_path='/etc/arinode/config.json'
service_path='/etc/systemd/system/arinode.service'

fail() { printf 'AriNode install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"; }

[[ $(id -u) -eq 0 ]] || fail 'run as root (curl ... | sudo bash)'
[[ $(uname -s) == Linux ]] || fail 'Linux is required'
for command in curl tar sha256sum install systemctl awk mktemp; do need "$command"; done

case "$(uname -m)" in
  x86_64|amd64) arch='amd64' ;;
  aarch64|arm64) arch='arm64' ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

asset="arinode-linux-${arch}.tar.gz"
staging=$(mktemp -d)
trap 'rm -rf -- "$staging"' EXIT

curl --fail --silent --show-error --location --retry 3 --proto '=https' --proto-redir '=https' \
  "${release_base}/SHA256SUMS" -o "${staging}/SHA256SUMS"
curl --fail --silent --show-error --location --retry 3 --proto '=https' --proto-redir '=https' \
  "${release_base}/${asset}" -o "${staging}/${asset}"

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "${staging}/SHA256SUMS")
[[ $expected =~ ^[[:xdigit:]]{64}$ ]] || fail "missing or invalid SHA256 for ${asset}"
actual=$(sha256sum "${staging}/${asset}")
[[ ${actual%% *} == "$expected" ]] || fail 'release SHA256 mismatch'

listing=$(tar -tzf "${staging}/${asset}")
[[ $listing == $'arinode\nanctl' || $listing == $'anctl\narinode' ]] || fail 'unexpected release archive contents'
tar -xzf "${staging}/${asset}" -C "$staging" --no-same-owner --no-same-permissions
for binary in arinode anctl; do
  [[ -f "${staging}/${binary}" && ! -L "${staging}/${binary}" ]] || fail "invalid ${binary} executable"
  chmod 755 "${staging}/${binary}"
done

version_line=$("${staging}/arinode" version)
[[ $version_line =~ ^AriNode[[:space:]]([0-9]{8}-[0-9a-f]{8,12})[[:space:]] ]] || fail 'release has an invalid version'
release_version=${BASH_REMATCH[1]}
[[ $("${staging}/anctl" version) == "$version_line" ]] || fail 'arinode and anctl versions differ'
printf 'Installing AriNode %s for linux/%s\n' "$release_version" "$arch"

if [[ -L $config_path ]]; then fail "refusing symlinked config: $config_path"; fi
if [[ ! -e $config_path ]]; then
  if [[ -z ${ARINODE_PANEL_URL:-} ]]; then
    [[ -r /dev/tty ]] || fail 'set ARINODE_PANEL_URL, ARINODE_PANEL_TOKEN and ARINODE_NODES for non-interactive installation'
    read -r -p 'Xboard panel URL: ' ARINODE_PANEL_URL </dev/tty
  fi
  if [[ -z ${ARINODE_PANEL_TOKEN:-} ]]; then
    [[ -r /dev/tty ]] || fail 'set ARINODE_PANEL_TOKEN for non-interactive installation'
    read -r -s -p 'Xboard server or machine token: ' ARINODE_PANEL_TOKEN </dev/tty
    printf '\n' >/dev/tty
  fi
  if [[ -z ${ARINODE_NODES:-} ]]; then
    [[ -r /dev/tty ]] || fail 'set ARINODE_NODES, for example "vless:1 trojan:2"'
    read -r -p 'Nodes (space separated, e.g. vless:1 trojan:2): ' ARINODE_NODES </dev/tty
  fi
  if [[ -z ${ARINODE_MACHINE_ID:-} && -r /dev/tty ]]; then
    read -r -p 'Xboard machine ID (Enter for server token): ' ARINODE_MACHINE_ID </dev/tty
  fi
  read -r -a nodes <<< "${ARINODE_NODES:-}"
  [[ ${#nodes[@]} -gt 0 ]] || fail 'at least one node is required'
  init_args=(init --panel "$ARINODE_PANEL_URL" --core "${ARINODE_CORE:-sing}" --output "${staging}/config.json")
  for node in "${nodes[@]}"; do init_args+=(--node "$node"); done
  if [[ -n ${ARINODE_MACHINE_ID:-} ]]; then init_args+=(--machine-id "$ARINODE_MACHINE_ID"); fi
  ARINODE_PANEL_TOKEN="$ARINODE_PANEL_TOKEN" "${staging}/anctl" "${init_args[@]}"
  unset ARINODE_PANEL_TOKEN
fi

install -d -m 755 /usr/local/bin
install -d -m 700 /etc/arinode
if [[ -f "${staging}/config.json" ]]; then
  install -m 600 "${staging}/config.json" "$config_path"
else
  printf 'Keeping existing %s\n' "$config_path"
fi

# Existing installations use the updater's rollback path rather than
# replacing executables while a node service may be running.
if [[ -x /usr/local/bin/arinode && -x /usr/local/bin/anctl && -e $service_path ]]; then
  /usr/local/bin/anctl upgrade
else
  install -m 755 "${staging}/arinode" /usr/local/bin/arinode
  install -m 755 "${staging}/anctl" /usr/local/bin/anctl
fi

if [[ ! -e $service_path ]]; then
  cat > "$service_path" <<'UNIT'
[Unit]
Description=AriNode node service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/arinode server --config /etc/arinode/config.json
WorkingDirectory=/etc/arinode
Restart=always
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
fi
systemctl daemon-reload
systemctl enable arinode.service
systemctl restart arinode.service
systemctl is-active --quiet arinode.service || fail 'service did not start; inspect journalctl -u arinode -e'
printf 'AriNode installed. Check: anctl status, anctl log, curl http://127.0.0.1:18086/v1/status\n'
printf 'The installer does not enable automatic upgrades. To opt in: sudo anctl upgrade auto enable\n'
