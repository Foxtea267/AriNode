# AriNode

AriNode 基于 V2bX 二次开发，保留 sing-box、Xray、Hysteria2 内核，并增加 Xboard 原生节点与机器认证、Komari 状态页面，以及 Linux TCP 调优入口。项目仓库为 [Foxtea267/AriNode](https://github.com/Foxtea267/AriNode)，上游来源与兼容边界见 [README.md](README.md)。

## 一键安装（Linux）

在使用 systemd 的 Linux 宿主机执行：

```sh
curl -fsSL https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh | sudo bash
```

脚本下载最新正式版、核对 SHA256 并安装程序，然后**先选择中文或英文，再选择部署方式**：从零部署、从已有 xbnode 迁移，或保留现有 AriNode 配置。检测到 `/etc/xboard-node/config.yml` 时默认选择迁移；迁移直接读取 xbnode 配置及同目录的 `credentials.env`，无需重新填写 Xboard URL、密钥和节点。已有 AriNode 配置会备份为 `.bak-时间戳`，切换服务失败时会尝试恢复 xbnode。选择从零部署时才会询问面板地址、密钥（输入不回显）和节点，例如 `vless:1 trojan:2`。配置文件权限为 `0600`，自动升级默认关闭。脚本需要 `curl`、`tar`、`sha256sum`、`systemctl` 和 Linux `amd64` 或 `arm64`。

安装后可运行 `sudo anctl bash` 重新进入语言与部署菜单；`anctl status`、`anctl log` 可查看状态。多 Xboard、Komari 面板可参考[多绑定配置](example/multi-bindings.config.json)编辑配置后执行 `sudo anctl restart`。

## 快速部署

1. 在 Xboard 中创建并启用节点，记录节点 ID。可选：创建机器并把节点绑定到机器。
2. 上传 [Xboard 插件](dist/arinode-xboard-plugin.zip)，启用 `ari_node`。管理员调用 `POST /api/v1/arinode/provision`，提交 `{"node_ids":[1,2],"core":"sing"}`。如使用机器专属密钥，再传 `"machine_id":3`。
3. 将接口返回的 JSON 保存为项目根目录的 `config.json`，并设置文件权限为 `0600`。也可复制 [配置示例](example/arinode.config.json)手工填写。
4. 运行 `docker compose up -d --build`，然后查看 `docker compose logs -f arinode`。
5. 上传 [Komari 插件](dist/arinode-komari-plugin.zip)。它会增加 AriNode 管理员页面，默认读取本机 `127.0.0.1:18086` 的状态接口。

本地构建需要 Go 1.25：

```sh
GOEXPERIMENT=jsonv2 go build -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' -o arinode .
GOEXPERIMENT=jsonv2 go build -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' -o anctl ./cmd/anctl
ARINODE_PANEL_TOKEN='XBOARD_TOKEN' ./anctl init --panel https://panel.example.com --node vless:1 --output config.json
./arinode server --config config.json
```

机器模式将 `ARINODE_PANEL_TOKEN` 设为机器 token，并在 `init` 后加 `--machine-id 3`。配置文件包含密钥，勿提交到版本库。

## 多节点隔离与多面板绑定

未填写 `Cores`、内核 `Type` 或节点 `Core` 时，默认使用 sing-box；`sing`、`singbox`、`sing-box` 均可填写，内部统一为 `sing`。指定 `CoreName` 时使用对应的命名内核。选择其他内核时需显式配置该内核及节点绑定。状态接口 `/v1/status` 会显示节点实际使用的 `core` 和监听端口 `port`；面板能收到上报仍需结合客户端连接验证代理可用性。

**xhttp / splithttp 例外**：当前内置的 sing-box 不支持这两种传输。没有指定 `CoreName` 的 sing-box 节点收到此配置时，会自动使用内置 Xray；优先复用已配置的 Xray，否则按需启动默认 Xray，不需要另装程序。普通 TCP 等传输仍使用 sing-box，面板切回普通传输后也会恢复原内核选择。显式指定 sing-box `CoreName` 的节点会提示选择 Xray。Xboard xhttp `extra` 中 PHP 生成的空对象数组也会自动兼容。若日志出现 `unknown transport type: xhttp`，运行 `sudo anctl upgrade`。

**客户端参数仍需正确**：AriNode 兼容面板空数组只作用于服务端，不能改写已经下发到客户端的订阅。若客户端报 `cannot unmarshal JSON array`，将对象字段的 `[]` 改为 `{}` 或删掉该空字段。没有独立下行服务器时删除 `extra.downloadSettings`；`address: null`、指向另一端口且 TLS/Reality 不一致的下行设置不能作为同节点的默认配置。可将[单节点 xhttp extra 示例](example/xhttp-extra.json)填入面板节点的 extra，保留节点自身的 host、path 和 Reality 参数，再更新客户端订阅。

**协议兼容**：支持 VMess、VLESS、Trojan、经典 SS、SS-2022、HY1、HY2、TUIC v5、AnyTLS、Mieru。面板的 `hysteria + version: 2` 会正确启动 HY2。Xray 用于 VMess、VLESS、Trojan、SS，其余协议使用 `sing`。各协议的实际转发、UDP 和流量统计验证范围见[协议测试矩阵](docs/protocol-compatibility.md)。

**Mieru**：节点填写 `NodeType: "mieru"`，使用 `sing` 内核或省略 `Core`。程序内置 [Mieru v3.29.0](https://github.com/enfein/mieru/tree/v3.29.0)，支持面板的 TCP/UDP 传输和 `traffic_pattern`；用户名和密码均为订阅用户 UUID，并接入 sing-box 路由、限额和流量上报。防火墙需放行节点对应的 TCP 或 UDP 端口。Xboard 插件已支持生成 Mieru 节点配置。

**机器节点自动同步**：机器绑定会在启动时及每 30 秒读取 `/api/v2/server/machine/nodes`。面板新增绑定后自动启动节点，成功读取绑定列表后会停用已解绑节点；未变更的节点保持连接，面板不可达或返回无效数据时保留现有节点。已写入 `Nodes` 的节点使用各自配置，新增节点继承同一机器第一个本地绑定的选项并使用独立运行标签。若只想管理本机明确填写的节点，在该机器的所有本地条目中设置 `MachineAutoDiscover: false`。自动同步不会修改 `config.json`。

**TLS 默认配置**：省略 `CertConfig` 时使用 `CertMode: "auto"`。仅当面板明确设置 `tls_settings.allow_insecure: true` 时自动生成内存中的自签证书；要求客户端校验证书时，应配置 `file`、`http`、`dns` 或 `self`，auto 模式会给出明确错误。显式配置 `none` 仍表示关闭 TLS，不能用于 HY/TUIC/AnyTLS 等必须使用 TLS 的协议。已有显式证书配置保持有效。TUIC 使用面板提供的 ALPN，未提供时为 `h3`；客户端应与之匹配。已兼容面板网络参数/headers 的 PHP 空数组和 Reality 字段的数字/字符串格式。

Xboard 的 `block` 规则支持普通域名及 `*.example.com`（匹配该域名及子域名）、IP/CIDR、`domain:`、`full:`、`keyword:`、`regexp:` 和 `protocol:`。普通域名按域名后缀处理；正则表达式须加 `regexp:` 前缀。规则更新先完整校验再替换，无效更新保留原有规则。旧版本如出现 `invalid domain rule` 且节点为 `retrying`、端口没有监听，请运行 `sudo anctl upgrade` 获取修复。

多个节点可以分别绑定到不同的 Xboard 面板；参考 [多绑定配置示例](example/multi-bindings.config.json)。在 `Panels` 中为每个面板指定唯一 `Name`、`ApiHost` 和 `ApiKey`（也可使用 `ApiKeyEnv`）；`Nodes` 中通过 `Panel` 引用。机器 token 可在对应面板项中填写 `MachineID`。不同 Xboard 即使使用相同机器 ID，也会分别上报机器状态。

节点启动失败时不会停止其他节点；失败绑定每 30 秒重试。配置热更新只重启变更的绑定，未变更节点继续运行。`/v1/status` 会显示各节点的 `running` 或 `retrying` 状态。修改 `Cores` 内核配置后需运行 `sudo anctl restart`。

当前隔离覆盖单节点的配置、面板请求、启动、更新错误及后台任务 panic。所有绑定仍共享 AriNode 进程；底层内核导致整个进程退出时，需使用每节点独立进程部署才能实现进程级隔离。

`Komari` 数组支持同时绑定多个 Komari 面板，每项填写唯一 `Name`、`Endpoint` 和该面板创建的客户端 `Token`（或 `TokenEnv`）。AriNode 独立向每个面板的 Agent v2 HTTP 接口上报主机数据；一个面板不可用不会中断其他上报或 Xboard 节点。此接口只提供监控上报，不处理 Komari 远程命令和 WebSSH；Komari 插件仍可单独安装在各面板查看 AriNode 状态。

原生 systemd 部署时，把 `arinode` 和 `anctl` 都安装到 `/usr/local/bin/`：`sudo install -m 755 arinode anctl /usr/local/bin/`。服务仍由 `arinode.service` 运行；日常管理使用 `sudo anctl start`、`sudo anctl stop`、`sudo anctl restart`、`anctl status` 和 `anctl log`。

## 正式版与升级

正式版版本号为 `YYYYMMDD-commit`，例如 `20261001-abcdef12`。日期采用发布提交的 UTC 日期，后缀为该提交的短 Git 哈希；自行构建且未注入版本号时显示 `dev`。维护者将代码提交到 `main` 后，可在 GitHub Actions 手动运行 **Test and release AriNode**，或为 `main` 上的提交推送符合规则的标签；测试通过才会发布正式版。发布包提供 Linux `amd64`、`arm64` 的 `arinode` 和 `anctl`。

原生 Linux systemd 安装可用：

```sh
anctl version
anctl upgrade check
sudo anctl upgrade
anctl upgrade auto status
sudo anctl upgrade auto enable
sudo anctl upgrade auto disable
```

**自动升级默认关闭。**执行 `auto enable` 后才会安装并启用每日检查的 systemd 定时器；`auto disable` 会停用并移除。升级仅选择本仓库最新的正式 Release，校验 GitHub 提供的 SHA256 摘要、包内容和两个程序内置版本后替换文件；若替换或服务重启失败，会恢复旧程序并尝试重启原服务。配置文件不受影响。Docker 部署请通过容器镜像更新，定时器用于原生 Linux systemd 安装。

## 与 Xboard-Node（xbnode）双向迁移

在已安装两套服务的 Linux 主机上，单条命令即可转换配置并切换 systemd 服务：

```sh
sudo anctl migrate from-xbnode --switch
sudo anctl migrate to-xbnode --switch
```

两条命令默认分别使用 `/etc/xboard-node/config.yml` 和 `/etc/arinode/config.json`。迁入时也会读取 xbnode 配置同目录的 `credentials.env`，解析其中的密钥变量，不执行文件中的命令。目标文件已存在时先加 `--force`；命令会以 `.bak-时间戳` 保存旧配置。可先用 `--dry-run` 查看节点数量及迁移警告，不会输出密钥或写入文件。只转换配置而不切换服务时省略 `--switch`；自定义路径使用 `--input`、`--output`。

迁入时会自动从面板查询没有写 `node_type` 的单节点配置，并从机器接口获取当前绑定节点。离线迁入可用 `--offline --node-type vless`，机器模式需要在线发现。迁出机器模式时 Xboard-Node 恢复为动态发现，因此会管理面板上该机器的全部节点。Xboard-Node 的 WebSocket、轮询与 Go 运行时设置，以及两端的部分内核自定义配置无法等价转换；命令会警告或拒绝这些场景。证书文件路径不会复制证书文件，请确认目标服务可读取原路径。`--switch` 要求目标 systemd 单元已经安装，启动失败时尝试重新启动原服务。

## 一键调优

在 Linux **宿主机**运行：

```sh
sudo bash scripts/tune.sh bbr
sudo bash scripts/tune.sh tcpfit
sudo bash scripts/tune.sh status
```

BBR 命令检查内核支持、写入独立的 sysctl 文件并应用 `bbr + fq`。tcpfit 命令下载固定版本、校验 SHA256 后运行上游安装菜单。

## 当前范围

已实现 Xboard UniProxy 节点接口、机器专属 token 的 `/api/v2/server/*` 节点接口、机器节点自动同步、机器负载上报和多节点配置下发。Xboard-Node 的 WebSocket 推送及握手/合并上报暂未接入。Komari 采用 Agent v2 HTTP 上报，插件提供额外的状态查看页面。
