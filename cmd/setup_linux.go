package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const (
	setupConfig  = "/etc/arinode/config.json"
	xbnodeConfig = "/etc/xboard-node/config.yml"
)

func init() {
	command.AddCommand(&cobra.Command{
		Use: "bash", Aliases: []string{"setup"},
		Short: "Interactive installation and migration menu",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runSetupMenu() },
	})
}

func runSetupMenu() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("run with sudo: sudo anctl bash")
	}
	interactive := setupIsTerminal()
	language := strings.ToLower(strings.TrimSpace(os.Getenv("ARINODE_LANGUAGE")))
	if language == "" {
		if !interactive {
			return fmt.Errorf("interactive terminal required, or set ARINODE_LANGUAGE and ARINODE_INSTALL_MODE")
		}
		fmt.Print("Language / 语言: 1) 中文  2) English [1]: ")
		choice, err := setupReadLine()
		if err != nil {
			return err
		}
		switch choice {
		case "", "1":
			language = "zh"
		case "2":
			language = "en"
		default:
			return fmt.Errorf("invalid language / 无效语言")
		}
	}
	if language != "zh" && language != "en" {
		return fmt.Errorf("ARINODE_LANGUAGE must be zh or en")
	}
	zh := language == "zh"
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ARINODE_INSTALL_MODE")))
	if mode == "" {
		if !interactive {
			return fmt.Errorf("set ARINODE_INSTALL_MODE=fresh, migrate or keep")
		}
		defaultMode, defaultChoice := "fresh", "1"
		if _, err := os.Stat(xbnodeConfig); err == nil {
			defaultMode, defaultChoice = "migrate", "2"
		} else if _, err := os.Stat(setupConfig); err == nil {
			defaultMode, defaultChoice = "keep", "3"
		}
		if zh {
			fmt.Printf("部署方式: 1) 从零部署  2) 从现有 xbnode 迁移  3) 保留现有 AriNode 配置 [%s]: ", defaultChoice)
		} else {
			fmt.Printf("Deployment: 1) Fresh setup  2) Migrate existing xbnode  3) Keep AriNode config [%s]: ", defaultChoice)
		}
		choice, err := setupReadLine()
		if err != nil {
			return err
		}
		switch choice {
		case "1":
			mode = "fresh"
		case "":
			mode = defaultMode
		case "2":
			mode = "migrate"
		case "3":
			mode = "keep"
		default:
			return fmt.Errorf("invalid deployment choice / 无效部署选项")
		}
	}
	switch mode {
	case "fresh":
		return setupFresh(zh, interactive)
	case "migrate":
		input := strings.TrimSpace(os.Getenv("ARINODE_XBNODE_CONFIG"))
		if input == "" {
			input = xbnodeConfig
		}
		if _, err := os.Stat(input); err != nil {
			return fmt.Errorf("xbnode config %s: %w", input, err)
		}
		if zh {
			fmt.Printf("正在迁移 %s；已有 AriNode 配置将备份。\n", input)
		} else {
			fmt.Printf("Migrating %s; any existing AriNode config will be backed up.\n", input)
		}
		return runMigration(input, setupConfig, setupConfig, "", true, false, false, true, true)
	case "keep":
		if _, err := os.Stat(setupConfig); err != nil {
			return fmt.Errorf("existing AriNode config %s: %w", setupConfig, err)
		}
		if err := systemctl("is-active", "--quiet", "xboard-node.service"); err == nil {
			if err := switchSystemd("xboard-node.service", "arinode.service"); err != nil {
				return err
			}
		} else {
			if err := systemctl("enable", "arinode.service"); err != nil {
				return err
			}
			if err := systemctl("restart", "arinode.service"); err != nil {
				return err
			}
		}
		if err := systemctl("is-active", "--quiet", "arinode.service"); err != nil {
			return err
		}
		if zh {
			fmt.Println("已保留配置并启动 AriNode。")
		} else {
			fmt.Println("Existing configuration kept; AriNode started.")
		}
		return nil
	default:
		return fmt.Errorf("ARINODE_INSTALL_MODE must be fresh, migrate or keep")
	}
}

func setupFresh(zh, interactive bool) error {
	panel, err := setupValue("ARINODE_PANEL_URL", "Xboard 面板地址: ", "Xboard panel URL: ", zh, interactive, false)
	if err != nil {
		return err
	}
	token, err := setupValue("ARINODE_PANEL_TOKEN", "Xboard 服务器或机器密钥: ", "Xboard server or machine token: ", zh, interactive, true)
	if err != nil {
		return err
	}
	nodes, err := setupValue("ARINODE_NODES", "节点（空格分隔，如 vless:1 trojan:2）: ", "Nodes (space separated, e.g. vless:1 trojan:2): ", zh, interactive, false)
	if err != nil {
		return err
	}
	machine := strings.TrimSpace(os.Getenv("ARINODE_MACHINE_ID"))
	if machine == "" && interactive {
		if zh {
			fmt.Print("机器 ID（服务器密钥请回车跳过）: ")
		} else {
			fmt.Print("Machine ID (Enter for server token): ")
		}
		machine, err = setupReadLine()
		if err != nil {
			return err
		}
	}
	machineID := 0
	if machine != "" {
		machineID, err = strconv.Atoi(machine)
		if err != nil {
			return fmt.Errorf("invalid machine ID: %w", err)
		}
	}
	core := strings.TrimSpace(os.Getenv("ARINODE_CORE"))
	if core == "" {
		core = "sing"
	}
	staging, err := os.MkdirTemp("", "arinode-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0700); err != nil {
		return err
	}
	tmpConfig := filepath.Join(staging, "config.json")
	cluster, err := clusterValues(os.Getenv("ARINODE_CLUSTER_DOMAIN"), os.Getenv("ARINODE_CLUSTER_MEMBER"))
	if err != nil {
		return err
	}
	if err := writeInitialConfigWithCluster(panel, token, core, tmpConfig, strings.Fields(nodes), machineID, false, cluster); err != nil {
		return err
	}
	data, err := os.ReadFile(tmpConfig)
	if err != nil {
		return err
	}
	backup, err := writeMigrated(setupConfig, data, true)
	if err != nil {
		return err
	}
	if backup != "" {
		fmt.Printf("Previous config backed up to %s\n", backup)
	}
	// A fresh setup on a host already running xbnode must release its ports.
	if err := systemctl("is-active", "--quiet", "xboard-node.service"); err == nil {
		if err := switchSystemd("xboard-node.service", "arinode.service"); err != nil {
			return err
		}
	} else {
		if err := systemctl("enable", "arinode.service"); err != nil {
			return err
		}
		if err := systemctl("restart", "arinode.service"); err != nil {
			return err
		}
		if err := systemctl("is-active", "--quiet", "arinode.service"); err != nil {
			return err
		}
	}
	if zh {
		fmt.Println("AriNode 已完成从零部署。")
	} else {
		fmt.Println("AriNode fresh setup complete.")
	}
	return nil
}

func setupValue(envKey, zhPrompt, enPrompt string, zh, interactive, secret bool) (string, error) {
	value := strings.TrimSpace(os.Getenv(envKey))
	if value != "" {
		return value, nil
	}
	if !interactive {
		return "", fmt.Errorf("%s is required without an interactive terminal", envKey)
	}
	if zh {
		fmt.Print(zhPrompt)
	} else {
		fmt.Print(enPrompt)
	}
	var err error
	if secret {
		var data []byte
		data, err = setupReadPassword()
		fmt.Println()
		value = strings.TrimSpace(string(data))
	} else {
		value, err = setupReadLine()
	}
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s cannot be empty", envKey)
	}
	return value, nil
}

func setupIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}

func setupReadPassword() ([]byte, error) {
	fd := int(os.Stdin.Fd())
	state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	hidden := *state
	hidden.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &hidden); err != nil {
		return nil, err
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, state)
	line, err := setupReadLine()
	return []byte(line), err
}

func setupReadLine() (string, error) {
	var data []byte
	var one [1]byte
	for {
		n, err := os.Stdin.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return strings.TrimSpace(string(data)), nil
			}
			if len(data) >= 4096 {
				return "", fmt.Errorf("input is too long")
			}
			data = append(data, one[0])
		}
		if err != nil {
			return "", err
		}
	}
}
