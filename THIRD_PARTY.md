# Third-party components

AriNode embeds the official [Mieru](https://github.com/enfein/mieru/tree/v3.29.0) transport library at v3.29.0. Mieru is copyright its authors and licensed under [GPL-3.0-or-later](https://github.com/enfein/mieru/blob/v3.29.0/LICENSE). Its sources are obtained through the pinned Go module in `go.mod` and `go.sum`; the AriNode adapter is in `core/sing/mieru.go`.

Other embedded cores and libraries retain their upstream licenses. The complete dependency list and versions are in `go.mod` and `go.sum`. The root MPL-2.0 license describes the original V2bX source; it does not replace dependency licenses.
