#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo 'Usage: sudo bash scripts/tune.sh {bbr|tcpfit|status}'
}

require_root() {
  if [[ $(id -u) -ne 0 ]]; then
    echo 'Run as root.' >&2
    exit 1
  fi
}

install_bbr() {
  require_root
  [[ $(uname -s) == Linux ]] || { echo 'Linux is required.' >&2; exit 1; }
  if ! sysctl -n net.ipv4.tcp_available_congestion_control | grep -qw bbr; then
    modprobe tcp_bbr 2>/dev/null || true
  fi
  sysctl -n net.ipv4.tcp_available_congestion_control | grep -qw bbr || {
    echo 'This kernel does not expose BBR.' >&2
    exit 1
  }
  if ! command -v tc >/dev/null; then
    echo 'iproute2 (tc) is required for fq.' >&2
    exit 1
  fi
  local target=/etc/sysctl.d/90-arinode-bbr.conf
  if [[ -f $target ]]; then
    cp -a "$target" "$target.backup.$(date +%Y%m%d%H%M%S)"
  fi
  printf '%s\n' 'net.core.default_qdisc = fq' 'net.ipv4.tcp_congestion_control = bbr' > "$target"
  sysctl -p "$target"
  [[ $(sysctl -n net.ipv4.tcp_congestion_control) == bbr ]] || {
    echo 'BBR was not applied.' >&2
    exit 1
  }
  echo 'BBR + fq configured. Existing interfaces may need to be recreated or rebooted to adopt fq.'
}

install_tcpfit() {
  require_root
  command -v curl >/dev/null || { echo 'curl is required.' >&2; exit 1; }
  command -v sha256sum >/dev/null || { echo 'sha256sum is required.' >&2; exit 1; }
  local version=v0.3.8
  local base="https://github.com/Kylin010/tcpfit/releases/download/$version"
  local staging
  staging=$(mktemp -d)
  trap 'rm -rf "$staging"' EXIT
  for file in tcpfit.sh install.sh SHA256SUMS; do
    curl --fail --location --silent --show-error "$base/$file" -o "$staging/$file"
  done
  (cd "$staging" && sha256sum --check SHA256SUMS)
  (cd "$staging" && bash tcpfit.sh)
}

case "${1:-}" in
  bbr) install_bbr ;;
  tcpfit) install_tcpfit ;;
  status)
    sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc
    command -v tcpfit >/dev/null && echo "tcpfit: $(command -v tcpfit)" || true
    ;;
  *) usage; exit 2 ;;
esac
