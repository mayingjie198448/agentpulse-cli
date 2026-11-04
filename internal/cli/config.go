package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var configFilePath string

	cmd := &cobra.Command{
		Use:   "config",
		Short: "交互式配置向导（设置 Broker、机器标识及本机 Agent）",
		Long: `运行交互式向导，按顺序配置：
  1. MQTT Broker 地址与鉴权信息（自动测连）
  2. 本机机器标识 machine_id
  3. 保存配置并发送 Announce 广播，扫描展示网内其他设备与 CLI
  4. 探测本机常见 Agent（agy、opencode、codex）并配置别名

配置文件默认保存至 ~/.config/agentpulse/config.yaml（Windows 为 %AppData%\agentpulse\config.yaml），
目录权限为 0700，文件权限为 0600。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFilePath == "" {
				if fl, _ := cmd.Flags().GetString("config"); fl != "" {
					configFilePath = fl
				}
			}
			return RunConfigWizard(cmd.InOrStdin(), cmd.OutOrStdout(), configFilePath)
		},
	}

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

// RunConfigWizard executes the interactive 4-step configuration wizard.
func RunConfigWizard(in io.Reader, out io.Writer, configPath string) error {
	reader := bufio.NewReader(in)

	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	fmt.Fprintln(out, "============================================================")
	fmt.Fprintln(out, "AgentPulse 0.5.1 交互式配置向导")
	fmt.Fprintf(out, "目标配置文件: %s\n", configPath)
	fmt.Fprintln(out, "============================================================")
	fmt.Fprintln(out)

	// Load existing configuration if available
	existingCfg, _ := config.LoadConfig(configPath)
	if existingCfg == nil {
		existingCfg = &config.Config{
			Version: 1,
			Rev:     1,
			Broker: config.BrokerConfig{
				URL: "tls://broker.example:8883",
				TLS: true,
			},
			Agents: config.DefaultAgents(),
		}
	}

	// ------------------------------------------------------------
	// 步骤 1: Broker 配置（含测连）
	// ------------------------------------------------------------
	fmt.Fprintln(out, "[步骤 1/4] 配置 MQTT Broker 连接")
	fmt.Fprintln(out, "------------------------------------------------------------")

	var brokerURL, username, password string
	var useTLS bool

	for {
		defaultURL := existingCfg.Broker.URL
		if defaultURL == "" {
			defaultURL = "tls://broker.example:8883"
		}
		brokerURL = promptLine(reader, out, "MQTT Broker 地址 (支持 tls://, mqtts://, tcp://)", defaultURL)

		defaultUser := existingCfg.Broker.Username
		username = promptLine(reader, out, "用户名 (无鉴权请留空)", defaultUser)

		defaultPass := existingCfg.Broker.Password
		password = promptLine(reader, out, "密码 (无密码请留空)", defaultPass)

		defaultTLS := existingCfg.Broker.TLS
		if strings.HasPrefix(brokerURL, "tls://") || strings.HasPrefix(brokerURL, "mqtts://") || strings.Contains(brokerURL, ":8883") {
			defaultTLS = true
		}
		useTLS = promptBool(reader, out, "启用 TLS 加密连接", defaultTLS)

		// Test connection
		fmt.Fprintf(out, "\n[...] 正在测试 Broker 连接 (%s)...\n", brokerURL)
		testCfg := mqtt.DefaultConfig()
		testCfg.BrokerURL = brokerURL
		testCfg.Username = username
		testCfg.Password = password
		testCfg.TLSEnable = useTLS
		testCfg.Timeout = 3 * time.Second

		testErr := testMQTTConnection(testCfg, 3*time.Second)
		if testErr == nil {
			fmt.Fprintln(out, "[✓] Broker 连接测试成功！")
			fmt.Fprintln(out)
			break
		}

		fmt.Fprintf(out, "[!] 连接测试失败: %v\n", testErr)
		if !promptBool(reader, out, "是否重新输入 Broker 信息？", true) {
			fmt.Fprintln(out, "[WARN] 保留当前 Broker 配置，请后续通过 'agentpulse doctor' 验证网络。")
			fmt.Fprintln(out)
			break
		}
		fmt.Fprintln(out)
	}

	// ------------------------------------------------------------
	// 步骤 2: machine_id 配置
	// ------------------------------------------------------------
	fmt.Fprintln(out, "[步骤 2/4] 配置本机标识 machine_id")
	fmt.Fprintln(out, "------------------------------------------------------------")

	defaultMachineID := existingCfg.MachineID
	if defaultMachineID == "" {
		host, err := os.Hostname()
		if err == nil && host != "" {
			defaultMachineID = wrapper.SanitizeID(host)
		} else {
			defaultMachineID = "macbook"
		}
	}

	rawMachineID := promptLine(reader, out, "本机机器标识 (小写字母、数字及连字符)", defaultMachineID)
	machineID := wrapper.SanitizeID(rawMachineID)
	if machineID == "" {
		machineID = "machine-1"
	}
	fmt.Fprintf(out, "[✓] 机器标识已确认为: %s\n\n", machineID)

	// ------------------------------------------------------------
	// 步骤 3: 保存配置并发送 Announce，展示网内设备/CLI
	// ------------------------------------------------------------
	fmt.Fprintln(out, "[步骤 3/4] 保存初始配置并发现网内设备")
	fmt.Fprintln(out, "------------------------------------------------------------")

	cfg := &config.Config{
		Version:   1,
		MachineID: machineID,
		Rev:       existingCfg.Rev + 1,
		Broker: config.BrokerConfig{
			URL:      brokerURL,
			Username: username,
			Password: password,
			TLS:      useTLS,
		},
		Agents: existingCfg.Agents,
	}
	if cfg.Rev <= 0 {
		cfg.Rev = 1
	}

	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("保存配置文件失败: %w", err)
	}
	fmt.Fprintf(out, "[✓] 配置文件已安全保存至: %s (权限: 0600)\n", configPath)

	// Publish initial Announce & discover other machines
	host, _ := os.Hostname()
	initialAnnounce := model.MachineAnnounce{
		V:         1,
		MachineID: machineID,
		TS:        time.Now().UTC(),
		Rev:       cfg.Rev,
		Hostname:  host,
		OS:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Agents:    nil,
	}

	// Try to connect to broker and scan network
	mqttCfg := mqtt.DefaultConfig()
	mqttCfg.BrokerURL = brokerURL
	mqttCfg.Username = username
	mqttCfg.Password = password
	mqttCfg.TLSEnable = useTLS
	mqttCfg.Timeout = 3 * time.Second
	mqttCfg.SettleTimeout = 500 * time.Millisecond

	st, err := mqtt.New(mqttCfg)
	if err == nil {
		defer st.Close()
		// Publish announce
		opts := paho.NewClientOptions()
		_ = mqtt.ApplyClientOptions(opts, mqttCfg)
		c := paho.NewClient(opts)
		if token := c.Connect(); token.WaitTimeout(2*time.Second) && token.Error() == nil {
			_ = mqtt.PublishAnnounce(c, initialAnnounce)
			c.Disconnect(100)
			fmt.Fprintln(out, "[✓] 已成功向 Broker 广播本机 Announce 注册信息")
		}

		// Query discovered machines and announces
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		discoveredAnnounces, _ := st.ListAnnounces(ctx)
		discoveredMachines, _ := st.ListMachines(ctx)

		var otherNodes []string
		for _, a := range discoveredAnnounces {
			if a.MachineID != machineID {
				var agentSummaries []string
				for _, ag := range a.Agents {
					sym := "✗"
					if ag.Ok {
						sym = "✓"
					}
					agentSummaries = append(agentSummaries, fmt.Sprintf("%s:%s", ag.ID, sym))
				}
				summary := "无 CLI 上报"
				if len(agentSummaries) > 0 {
					summary = strings.Join(agentSummaries, " ")
				}
				otherNodes = append(otherNodes, fmt.Sprintf("  • %-12s (%s, %s) CLI: [%s]", a.MachineID, a.Hostname, a.OS, summary))
			}
		}

		if len(otherNodes) > 0 {
			fmt.Fprintf(out, "\n[网内已发现 %d 台其他设备]:\n", len(otherNodes))
			for _, n := range otherNodes {
				fmt.Fprintln(out, n)
			}
		} else if len(discoveredMachines) > 1 {
			fmt.Fprintf(out, "\n[网内发现 %d 台在线节点，正在等待其 CLI Announce 上报]:\n", len(discoveredMachines)-1)
			for _, m := range discoveredMachines {
				if m.ID != machineID {
					fmt.Fprintf(out, "  • %-12s (在线: %v, 心跳: %s)\n", m.ID, m.Online, m.HeartbeatAgo())
				}
			}
		} else {
			fmt.Fprintln(out, "\n[提示] 当前网内暂无其他在线设备（其他机器启动 agentpulse 后将自动发现并汇入拓扑）。")
		}
	} else {
		fmt.Fprintf(out, "[提示] 未能连上 Broker 扫描拓扑 (%v)；稍后启动服务时将自动重试。\n", err)
	}
	fmt.Fprintln(out)

	// ------------------------------------------------------------
	// 步骤 4: 本机 agents + 别名探测与配置
	// ------------------------------------------------------------
	fmt.Fprintln(out, "[步骤 4/4] 本机 Agent CLI 探测与别名配置")
	fmt.Fprintln(out, "------------------------------------------------------------")
	fmt.Fprintln(out, "正在探测本机 PATH 中的常见 Agent (agy, opencode, codex)...")

	defaultAgents := config.DefaultAgents()
	probed := config.ProbeAgents(defaultAgents)

	var agentAnnounces []model.AgentAnnounce
	for _, p := range probed {
		mark := "✗ 未找到"
		if p.Ok {
			mark = fmt.Sprintf("✓ 可用 (%s)", p.Detail)
		}
		fmt.Fprintf(out, "  • %-10s [别名: %-16s] : %s\n", p.ID, strings.Join(p.Alias, ", "), mark)

		agentAnnounces = append(agentAnnounces, model.AgentAnnounce{
			ID:     p.ID,
			Alias:  p.Alias,
			Ok:     p.Ok,
			Detail: p.Detail,
		})
	}
	fmt.Fprintln(out)

	keepAgents := promptBool(reader, out, "是否采用上述探测到的 Agent 作为本机注册配置？", true)
	if keepAgents {
		cfg.Agents = defaultAgents
	}

	// Update config with agents and re-save
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}

	// Re-publish full announce with probed agents if connected
	finalAnnounce := model.MachineAnnounce{
		V:         1,
		MachineID: machineID,
		TS:        time.Now().UTC(),
		Rev:       cfg.Rev,
		Hostname:  host,
		OS:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Agents:    agentAnnounces,
	}

	opts := paho.NewClientOptions()
	_ = mqtt.ApplyClientOptions(opts, mqttCfg)
	c := paho.NewClient(opts)
	if token := c.Connect(); token.WaitTimeout(2*time.Second) && token.Error() == nil {
		_ = mqtt.PublishAnnounce(c, finalAnnounce)
		c.Disconnect(100)
		fmt.Fprintln(out, "[✓] 已将最新本机 Agent 探测状态发布至 Broker Announce")
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "============================================================")
	fmt.Fprintln(out, "🎉 AgentPulse 配置完成！")
	fmt.Fprintf(out, "配置文件路径: %s\n", configPath)
	fmt.Fprintln(out, "后续建议操作:")
	fmt.Fprintln(out, "  1. 运行 'agentpulse doctor' 验证本机环境与全网设备/CLI 扁平表")
	fmt.Fprintln(out, "  2. 运行 'agentpulse' 启动常驻心跳、命令监听与监控一体进程")
	fmt.Fprintln(out, "  3. 运行 'agentpulse dispatch <别名|CLI> -- <参数>' 远程派发任务")
	fmt.Fprintln(out, "============================================================")

	return nil
}
