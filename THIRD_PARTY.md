# Third-party components

AriNode embeds the official [Mieru](https://github.com/enfein/mieru/tree/v3.29.0) transport library at v3.29.0. Mieru is copyright its authors and licensed under [GPL-3.0-or-later](https://github.com/enfein/mieru/blob/v3.29.0/LICENSE). Its sources are obtained through the pinned Go module in `go.mod` and `go.sum`; the AriNode adapter is in `core/sing/mieru.go`.

Other embedded cores and libraries retain their upstream licenses. The complete dependency list and versions are in `go.mod` and `go.sum`. The root MPL-2.0 license describes the original V2bX source; it does not replace dependency licenses.

The AnyTLS adapter in `core/sing/anytls.go` uses the pinned [sing-anytls v0.0.11](https://github.com/anytls/sing-anytls/tree/v0.0.11) SDK and sing-box listener/TLS/UDP-over-TCP interfaces. It replaces the fork's user/session bookkeeping to enforce revocation; the protocol SDK remains upstream. The SDK is copyright 2025 anytls and retains its [GPL-3.0-or-later license](https://github.com/anytls/sing-anytls/blob/v0.0.11/LICENSE).
