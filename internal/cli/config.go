package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/autostart"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var configFilePath string
	var section string

	cmd := &cobra.Command{
		Use:   "config",
		Short: "交互式配置向导（设置机器标识、Broker 及本机 Agent）",
		Long: `运行交互式向导，配置 AgentPulse：
  1. 本机机器标识 machine_id
  2. MQTT Broker 地址与鉴权信息（自动测连）
  3. 本机 Agent CLI 路径、别名及启用状态（仅限本机）
  4. 写入 0600 权限 yaml 配置文件并立即运行连通性与 CLI 探测验证

若配置文件已存在，支持分段编辑（Broker / Agents / machine_id），显示当前值且按回车可保留。
配置文件默认保存至 ~/.config/agentpulse/config.yaml（Windows 为 %AppData%\agentpulse\config.yaml）。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFilePath == "" {
				if fl, _ := cmd.Flags().GetString("config"); fl != "" {
					configFilePath = fl
				}
			}
			return RunConfigWizardWithSection(cmd.InOrStdin(), cmd.OutOrStdout(), configFilePath, section)
		},
	}

	cmd.Flags().StringVar(&section, "section", "", "直接编辑指定配置分段 (broker, agents, machine_id, autostart, all)")
	cmd.AddCommand(newAutostartCmd())
	return cmd
}

// promptLine reads a line of user input, using defaultVal if the user simply presses Enter.
func promptLine(reader *bufio.Reader, out io.Writer, prompt string, defaultVal string) string {
	if defaultVal != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}

	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return defaultVal
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// promptBool asks a yes/no question with a default value.
func promptBool(reader *bufio.Reader, out io.Writer, prompt string, defaultVal bool) bool {
	var suffix string
	if defaultVal {
		suffix = "[Y/n]"
	} else {
		suffix = "[y/N]"
	}
	fmt.Fprintf(out, "%s %s: ", prompt, suffix)

	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return defaultVal
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return defaultVal
	}
	if line == "y" || line == "yes" || line == "true" || line == "1" {
		return true
	}
	if line == "n" || line == "no" || line == "false" || line == "0" {
		return false
	}
	return defaultVal
}

// RunConfigWizard executes the interactive configuration wizard.
func RunConfigWizard(in io.Reader, out io.Writer, configPath string) error {
	return RunConfigWizardWithSection(in, out, configPath, "")
}

// RunConfigWizardWithSection executes the wizard, optionally jumping straight to a specific section.
func RunConfigWizardWithSection(in io.Reader, out io.Writer, configPath string, section string) error {
	reader := bufio.NewReader(in)

	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	existingCfg, loadErr := config.LoadConfig(configPath)
	configExists := (loadErr == nil && existingCfg != nil)

	cfg := &config.Config{
		Version: 1,
		Rev:     1,
	}
	if configExists {
		cfg.Version = existingCfg.Version
		cfg.Rev = existingCfg.Rev + 1
		cfg.MachineID = existingCfg.MachineID
		cfg.Broker = existingCfg.Broker
		cfg.Agents = existingCfg.Agents
		cfg.Autostart = existingCfg.Autostart
	} else {
		cfg.Broker = config.BrokerConfig{
			URL: "tls://broker.example:8883",
			TLS: true,
		}
		cfg.Agents = config.DefaultAgents()
	}

	fmt.Fprintln(out, "============================================================")
	fmt.Fprintln(out, "AgentPulse 交互式配置向导")
	fmt.Fprintf(out, "目标配置文件: %s\n", configPath)
	fmt.Fprintln(out, "============================================================")

	// If config exists and no explicit section passed, show current values and prompt menu
	if configExists && section == "" {
		fmt.Fprintln(out, "检测到已存在配置文件。当前配置概览:")
		fmt.Fprintf(out, "  • 机器标识 (machine_id): %s\n", cfg.MachineID)
		authDesc := "匿名"
		if cfg.Broker.Username != "" {
			authDesc = fmt.Sprintf("用户名: %s", cfg.Broker.Username)
		}
		fmt.Fprintf(out, "  • MQTT Broker: %s (%s, TLS: %v)\n", cfg.Broker.URL, authDesc, cfg.Broker.TLS)

		var agentSummary []string
		for _, ag := range cfg.Agents {
			if ag.IsEnabled() {
				agentSummary = append(agentSummary, ag.ID)
			} else {
				agentSummary = append(agentSummary, ag.ID+"(已禁用)")
			}
		}
		if len(agentSummary) == 0 {
			agentSummary = []string{"无"}
		}
		fmt.Fprintf(out, "  • 本机 Agent: %s\n", strings.Join(agentSummary, ", "))

		autostartDesc := "未启用"
		if cfg.Autostart {
			autostartDesc = "已启用 (LaunchAgent)"
		}
		fmt.Fprintf(out, "  • 开机自启常驻后台: %s\n\n", autostartDesc)

		fmt.Fprintln(out, "请选择配置模式 (按回车默认编辑全部):")
		fmt.Fprintln(out, "  1) 完整重新配置 (按步骤配置全部项目)")
		fmt.Fprintln(out, "  2) 仅编辑 MQTT Broker 配置")
		fmt.Fprintln(out, "  3) 仅编辑 本机 Agent CLI 配置")
		fmt.Fprintln(out, "  4) 仅编辑 机器标识 (machine_id)")
		fmt.Fprintln(out, "  5) 仅编辑 开机自启常驻后台 (LaunchAgent)")
		choice := promptLine(reader, out, "请选择 [1/2/3/4/5]", "1")
		switch choice {
		case "2", "broker":
			section = "broker"
		case "3", "agents":
			section = "agents"
		case "4", "machine_id", "machine":
			section = "machine_id"
		case "5", "autostart", "daemon":
			section = "autostart"
		default:
			section = "all"
		}
		fmt.Fprintln(out)
	}

	section = strings.ToLower(strings.TrimSpace(section))
	switch section {
	case "broker":
		cfg.Broker = configureBrokerSection(reader, out, cfg.Broker)
	case "agents":
		cfg.Agents = configureAgentsSection(reader, out, cfg.Agents, true)
	case "machine_id":
		cfg.MachineID = configureMachineIDSection(reader, out, cfg.MachineID)
	case "autostart":
		cfg.Autostart = configureAutostartSection(reader, out, cfg.Autostart)
	default:
		// Guided all
		fmt.Fprintln(out, "[步骤 1/3] 配置本机机器标识 (machine_id)")
		cfg.MachineID = configureMachineIDSection(reader, out, cfg.MachineID)

		fmt.Fprintln(out, "[步骤 2/3] 配置 MQTT Broker 连接与鉴权")
		cfg.Broker = configureBrokerSection(reader, out, cfg.Broker)

		fmt.Fprintln(out, "[步骤 3/3] 配置本机 Agent CLI (agy, opencode 等)")
		cfg.Agents = configureAgentsSection(reader, out, cfg.Agents, false)

		if autostart.IsSupported() {
			fmt.Fprintln(out, "[开机自启] 配置常驻后台守护进程 (LaunchAgent)")
			cfg.Autostart = configureAutostartSection(reader, out, cfg.Autostart)
		}
	}

	// Write 0600 yaml
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("保存配置文件失败: %w", err)
	}
	fmt.Fprintf(out, "[✓] 配置文件已安全保存至: %s (权限: 0600)\n\n", configPath)

	// Synchronize autostart daemon / LaunchAgent state
	if autostart.IsSupported() {
		oldMachineID := ""
		oldBroker := config.BrokerConfig{}
		wasAutostart := false
		if existingCfg != nil {
			oldMachineID = existingCfg.MachineID
			oldBroker = existingCfg.Broker
			wasAutostart = existingCfg.Autostart
		}

		if cfg.Autostart {
			if wasAutostart && oldMachineID != "" && oldMachineID != cfg.MachineID {
				fmt.Fprintf(out, "[!] 检测到 machine_id 变更 (%s -> %s)，正在同步下线旧节点并重启常驻后台服务...\n", oldMachineID, cfg.MachineID)
				if err := autostart.PublishOffline(oldBroker, oldMachineID); err == nil {
					fmt.Fprintf(out, "  [✓] 已向 Broker 发布旧节点 %s 离线状态 (避免长效双在线)\n", oldMachineID)
				}
				if err := autostart.Restart(cfg, oldMachineID, ""); err != nil {
					fmt.Fprintf(out, "  [WARN] 重启常驻守护进程失败: %v\n", err)
				} else {
					fmt.Fprintln(out, "  [✓] 已重启常驻守护进程并更新配置 (LaunchAgent)")
				}
			} else if !wasAutostart {
				if err := autostart.Enable(cfg, ""); err != nil {
					fmt.Fprintf(out, "  [WARN] 安装 LaunchAgent 失败: %v\n", err)
				} else {
					fmt.Fprintln(out, "[✓] 已成功配置并启动 macOS 开机自启常驻守护进程 (LaunchAgent)")
				}
			} else {
				if oldBroker.URL != cfg.Broker.URL {
					_ = autostart.Restart(cfg, "", "")
				}
			}
		} else {
			if wasAutostart || autostart.Status().Installed {
				if oldMachineID != "" {
					_ = autostart.PublishOffline(oldBroker, oldMachineID)
				}
				if err := autostart.Disable(); err != nil {
					fmt.Fprintf(out, "  [WARN] 卸载 LaunchAgent 失败: %v\n", err)
				} else {
					fmt.Fprintln(out, "[✓] 已卸载 macOS 开机自启常驻后台 (LaunchAgent 已移除)")
				}
			}
		}
	}

	// Immediately verify: broker connect + CLI probe (reusing doctor logic)
	VerifyConfigDiagnostics(out, cfg)

	fmt.Fprintln(out, "============================================================")
	fmt.Fprintln(out, "🎉 AgentPulse 配置完成！")
	fmt.Fprintf(out, "配置文件路径: %s\n", configPath)
	fmt.Fprintln(out, "后续建议操作:")
	fmt.Fprintln(out, "  1. 运行 'agentpulse doctor' 验证本机环境与全网拓扑")
	fmt.Fprintln(out, "  2. 运行 'agentpulse' 启动常驻心跳与监控大盘")
	fmt.Fprintln(out, "  3. 运行 'agentpulse dispatch <别名|CLI> -- <参数>' 远程派发任务")
	fmt.Fprintln(out, "============================================================")

	return nil
}

func configureMachineIDSection(reader *bufio.Reader, out io.Writer, currentID string) string {
	fmt.Fprintln(out, "------------------------------------------------------------")
	defaultMachineID := currentID
	if defaultMachineID == "" {
		host, err := os.Hostname()
		if err == nil && host != "" {
			defaultMachineID = wrapper.SanitizeID(host)
		} else {
			defaultMachineID = "machine-1"
		}
	}

	rawID := promptLine(reader, out, "本机机器标识 (小写字母、数字及连字符)", defaultMachineID)
	machineID := wrapper.SanitizeID(rawID)
	if machineID == "" {
		machineID = "machine-1"
	}
	fmt.Fprintf(out, "[✓] 机器标识已确认为: %s\n\n", machineID)
	return machineID
}

func configureAutostartSection(reader *bufio.Reader, out io.Writer, current bool) bool {
	fmt.Fprintln(out, "------------------------------------------------------------")
	if !autostart.IsSupported() {
		fmt.Fprintf(out, "当前平台 (%s) 暂未支持开机自启常驻后台守护进程 (目前支持 macOS LaunchAgent)\n\n", runtime.GOOS)
		return false
	}
	st := autostart.Status()
	fmt.Fprintf(out, "当前后台自启状态: %s\n", st.Detail)
	enabled := promptBool(reader, out, "是否开启 macOS 用户开机自启 / 后台常驻守护进程 (LaunchAgent)", current)
	if enabled {
		fmt.Fprintln(out, "[✓] 开机自启常驻后台: 开启")
	} else {
		fmt.Fprintln(out, "[✓] 开机自启常驻后台: 关闭")
	}
	fmt.Fprintln(out)
	return enabled
}

func configureBrokerSection(reader *bufio.Reader, out io.Writer, current config.BrokerConfig) config.BrokerConfig {
	fmt.Fprintln(out, "------------------------------------------------------------")
	defaultURL := current.URL
	if defaultURL == "" {
		defaultURL = "tls://broker.example:8883"
	}
	brokerURL := promptLine(reader, out, "MQTT Broker 地址 (支持 tls://, mqtts://, tcp://)", defaultURL)

	defaultUser := current.Username
	username := promptLine(reader, out, "用户名 (无鉴权请留空)", defaultUser)

	defaultPass := current.Password
	password := promptLine(reader, out, "密码 (无密码请留空)", defaultPass)

	defaultTLS := current.TLS
	if strings.HasPrefix(brokerURL, "tls://") || strings.HasPrefix(brokerURL, "mqtts://") || strings.Contains(brokerURL, ":8883") {
		defaultTLS = true
	}
	useTLS := promptBool(reader, out, "启用 TLS 加密连接", defaultTLS)
	fmt.Fprintln(out)

	return config.BrokerConfig{
		URL:      brokerURL,
		Username: username,
		Password: password,
		TLS:      useTLS,
	}
}

func configureAgentsSection(reader *bufio.Reader, out io.Writer, currentAgents []config.AgentConfig, forceCustomize bool) []config.AgentConfig {
	fmt.Fprintln(out, "------------------------------------------------------------")
	fmt.Fprintln(out, "正在探测本机 PATH 中的常见 Agent...")

	if len(currentAgents) == 0 {
		currentAgents = config.DefaultAgents()
	}

	probed := config.ProbeAgents(currentAgents)
	for _, p := range probed {
		mark := "✗ 未找到"
		if p.Ok {
			mark = fmt.Sprintf("✓ 可用 (%s)", p.Detail)
		} else if strings.Contains(p.Detail, "禁用") {
			mark = "[-] 已禁用"
		}
		aliases := strings.Join(p.Alias, ", ")
		fmt.Fprintf(out, "  • %-10s [别名: %-14s] : %s\n", p.ID, aliases, mark)
	}
	fmt.Fprintln(out)

	customize := forceCustomize
	if !customize {
		customize = promptBool(reader, out, "是否逐项配置/自定义本机 Agent CLI（路径、别名及启用状态）？", false)
	}
	if !customize {
		fmt.Fprintln(out, "[✓] 已保留当前 Agent 配置。")
		fmt.Fprintln(out)
		return currentAgents
	}

	var updatedAgents []config.AgentConfig
	for _, ag := range currentAgents {
		fmt.Fprintf(out, "\n配置 Agent: %s\n", ag.ID)
		enabled := promptBool(reader, out, fmt.Sprintf("  是否启用 %s", ag.ID), ag.IsEnabled())
		var path string
		var aliases []string
		if enabled {
			path = promptLine(reader, out, "  可执行文件路径 (留空自动搜索 PATH)", ag.Path)
			defaultAliases := strings.Join(ag.Alias, ", ")
			aliasStr := promptLine(reader, out, "  命令别名 (多个别名用逗号隔开)", defaultAliases)
			for _, a := range strings.Split(aliasStr, ",") {
				trimmed := strings.TrimSpace(a)
				if trimmed != "" {
					aliases = append(aliases, trimmed)
				}
			}
		} else {
			path = ag.Path
			aliases = ag.Alias
		}
		updatedAgents = append(updatedAgents, config.AgentConfig{
			ID:      ag.ID,
			Path:    path,
			Alias:   aliases,
			Enabled: &enabled,
		})
	}

	// Optional add more agents
	for {
		addMore := promptBool(reader, out, "是否添加其他本地 Agent CLI？", false)
		if !addMore {
			break
		}
		customID := promptLine(reader, out, "Agent ID (如 myagent)", "")
		if customID == "" {
			break
		}
		path := promptLine(reader, out, "可执行文件路径 (留空搜索 PATH)", "")
		aliasStr := promptLine(reader, out, "命令别名 (逗号分隔)", customID)
		var aliases []string
		for _, a := range strings.Split(aliasStr, ",") {
			trimmed := strings.TrimSpace(a)
			if trimmed != "" {
				aliases = append(aliases, trimmed)
			}
		}
		t := true
		updatedAgents = append(updatedAgents, config.AgentConfig{
			ID:      customID,
			Path:    path,
			Alias:   aliases,
			Enabled: &t,
		})
	}

	fmt.Fprintln(out, "[✓] 本机 Agent 配置已更新。")
	fmt.Fprintln(out)
	return updatedAgents
}

// VerifyConfigDiagnostics runs the doctor verification logic:
// broker connectivity test + local agent CLI probe.
func VerifyConfigDiagnostics(out io.Writer, cfg *config.Config) (brokerOk bool, allAgentsOk bool) {
	fmt.Fprintln(out, "============================================================")
	fmt.Fprintln(out, "正在运行配置验证 (Doctor 自检)...")
	fmt.Fprintln(out, "============================================================")

	// 1. Broker connect
	fmt.Fprintf(out, "[1/2] 测试 MQTT Broker 连通性 (%s)...\n", cfg.Broker.URL)
	mqttCfg := mqtt.DefaultConfig()
	mqttCfg.BrokerURL = cfg.Broker.URL
	mqttCfg.Username = cfg.Broker.Username
	mqttCfg.Password = cfg.Broker.Password
	mqttCfg.TLSEnable = cfg.Broker.TLS
	mqttCfg.Timeout = 5 * time.Second

	testErr := testMQTTConnection(mqttCfg, mqttCfg.Timeout)
	if testErr == nil {
		fmt.Fprintln(out, "  [✓] Broker 连通性测试通过！")
		brokerOk = true
	} else {
		fmt.Fprintf(out, "  [!] Broker 连通性测试失败: %v (可稍后运行 'agentpulse doctor' 重新自检)\n", testErr)
	}

	// 2. CLI probe
	fmt.Fprintln(out, "\n[2/2] 本机 Agent CLI 探测...")
	agents := cfg.Agents
	if len(agents) == 0 {
		agents = config.DefaultAgents()
	}
	probed := config.ProbeAgents(agents)
	allAgentsOk = true
	var announceAgents []model.AgentAnnounce
	for _, p := range probed {
		aliases := strings.Join(p.Alias, ", ")
		if !p.Ok && strings.Contains(p.Detail, "禁用") {
			fmt.Fprintf(out, "  [-] %-10s [别名: %-14s] : %s\n", p.ID, aliases, p.Detail)
		} else if p.Ok {
			fmt.Fprintf(out, "  [✓] %-10s [别名: %-14s] : 可用 (%s)\n", p.ID, aliases, p.Detail)
		} else {
			fmt.Fprintf(out, "  [✗] %-10s [别名: %-14s] : %s\n", p.ID, aliases, p.Detail)
			allAgentsOk = false
		}
		announceAgents = append(announceAgents, model.AgentAnnounce{
			ID:     p.ID,
			Alias:  p.Alias,
			Ok:     p.Ok,
			Detail: p.Detail,
		})
	}

	// 3. Publish announce if broker connected and machineID set
	if brokerOk && cfg.MachineID != "" {
		host, _ := os.Hostname()
		ann := model.MachineAnnounce{
			V:         1,
			MachineID: cfg.MachineID,
			TS:        time.Now().UTC(),
			Rev:       cfg.Rev,
			Hostname:  host,
			OS:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			Agents:    announceAgents,
		}
		opts := paho.NewClientOptions()
		_ = mqtt.ApplyClientOptions(opts, mqttCfg)
		c := paho.NewClient(opts)
		if token := c.Connect(); token.WaitTimeout(mqttCfg.Timeout) && token.Error() == nil {
			_ = mqtt.PublishAnnounce(c, ann)
			c.Disconnect(100)
			fmt.Fprintln(out, "\n  [✓] 已将最新本机 Announce 注册信息发布至 Broker")
		}
	}
	fmt.Fprintln(out)

	return brokerOk, allAgentsOk
}
