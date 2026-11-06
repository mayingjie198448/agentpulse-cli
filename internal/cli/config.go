package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
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

	cmd.Flags().StringVar(&section, "section", "", "直接编辑指定配置分段 (broker, agents, machine_id, all)")
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
	} else {
		cfg.Broker = config.BrokerConfig{
			URL: "tls://broker.example:8883",
			TLS: true,
		}
		cfg.Agents = nil
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

		var builtinSummary, customSummary []string
		for _, ag := range cfg.Agents {
			desc := ag.ID
			if !ag.IsEnabled() {
				desc += "(已禁用)"
			}
			if ag.IsBuiltin() {
				builtinSummary = append(builtinSummary, desc)
			} else {
				customSummary = append(customSummary, desc)
			}
		}
		if len(builtinSummary) == 0 {
			builtinSummary = []string{"无"}
		}
		fmt.Fprintf(out, "  • 本机预置 Agent: %s\n", strings.Join(builtinSummary, ", "))
		if len(customSummary) > 0 {
			fmt.Fprintf(out, "  • 本机自定义 Agent: %s\n", strings.Join(customSummary, ", "))
		}
		fmt.Fprintln(out)

		fmt.Fprintln(out, "请选择配置模式 (按回车默认编辑全部):")
		fmt.Fprintln(out, "  1) 完整重新配置 (按步骤配置全部项目)")
		fmt.Fprintln(out, "  2) 仅编辑 MQTT Broker 配置")
		fmt.Fprintln(out, "  3) 仅编辑 本机 Agent CLI 配置")
		fmt.Fprintln(out, "  4) 仅编辑 机器标识 (machine_id)")
		choice := promptLine(reader, out, "请选择 [1/2/3/4]", "1")
		switch choice {
		case "2", "broker":
			section = "broker"
		case "3", "agents":
			section = "agents"
		case "4", "machine_id", "machine":
			section = "machine_id"
		default:
			section = "all"
		}
		fmt.Fprintln(out)
	}

	section = strings.ToLower(strings.TrimSpace(section))
	switch section {
	case "broker":
		if len(cfg.Agents) == 0 {
			cfg.Agents = config.DefaultAgents()
		}
		cfg.Broker = configureBrokerSection(reader, out, cfg.Broker)
	case "agents":
		cfg.Agents = configureAgentsSection(reader, out, cfg.Agents, true)
	case "machine_id":
		if len(cfg.Agents) == 0 {
			cfg.Agents = config.DefaultAgents()
		}
		cfg.MachineID = configureMachineIDSection(reader, out, cfg.MachineID)
	default:
		// Guided all
		fmt.Fprintln(out, "[步骤 1/3] 配置本机机器标识 (machine_id)")
		cfg.MachineID = configureMachineIDSection(reader, out, cfg.MachineID)

		fmt.Fprintln(out, "[步骤 2/3] 配置 MQTT Broker 连接与鉴权")
		cfg.Broker = configureBrokerSection(reader, out, cfg.Broker)

		fmt.Fprintln(out, "[步骤 3/3] 配置本机 Agent CLI (扫描、勾选预置支持引擎与自定义追加)")
		cfg.Agents = configureAgentsSection(reader, out, cfg.Agents, !configExists)
	}

	// Write 0600 yaml
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("保存配置文件失败: %w", err)
	}
	fmt.Fprintf(out, "[✓] 配置文件已安全保存至: %s (权限: 0600)\n\n", configPath)

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
	fmt.Fprintln(out, "【本机 Agent CLI 配置向导】")

	// If there are existing agents and not forceCustomize, show them and ask if user wants to reconfigure
	if len(currentAgents) > 0 && !forceCustomize {
		fmt.Fprintln(out, "当前配置的本机 Agent 状态:")
		probed := config.ProbeAgents(currentAgents)
		for _, p := range probed {
			mark := "✗ 未找到"
			if p.Ok {
				mark = fmt.Sprintf("✓ 可用 (%s)", p.Detail)
			} else if strings.Contains(p.Detail, "禁用") {
				mark = "[-] 已禁用"
			}
			aliases := strings.Join(p.Alias, ", ")
			customTag := ""
			if p.Custom {
				customTag = " [自定义]"
			}
			fmt.Fprintf(out, "  • %-10s [别名: %-14s]%s : %s\n", p.ID, aliases, customTag, mark)
		}
		fmt.Fprintln(out)

		reconfigure := promptBool(reader, out, "是否运行向导重新扫描并配置本机 Agent CLI？", false)
		if !reconfigure {
			fmt.Fprintln(out, "[✓] 已保留当前 Agent 配置。")
			fmt.Fprintln(out)
			return currentAgents
		}
	}

	// 1. Automatic scan (PATH + common install locations)
	fmt.Fprintln(out, "\n[步骤 1/4] 自动扫描本机已安装的 Agent CLI (PATH 与常见安装路径)")
	doScan := promptBool(reader, out, "是否自动扫描本机已安装的 Agent CLI？", true)
	var scanRes config.ScanResult
	if doScan {
		for {
			fmt.Fprintln(out, "正在扫描本机 PATH 及常见安装路径...")
			scanRes = config.ScanLocalAgents()
			printScanSummary(out, scanRes)
			rescan := promptBool(reader, out, "是否重新扫描？", false)
			if !rescan {
				break
			}
		}
	} else {
		fmt.Fprintln(out, "[✓] 已跳过自动扫描。")
	}

	// 2. Multi-select built-in candidate engines
	fmt.Fprintln(out, "\n[步骤 2/4] 勾选预置支持引擎")
	fmt.Fprintln(out, "预置候选引擎列表 (支持自动路径探测与通用命令调度；心跳/流式/派活深度协议按各引擎能力逐步适配):")

	builtinDefs := config.BuiltinEngineDefs()
	discoveredMap := make(map[string]config.DiscoveredAgent)
	for _, d := range scanRes.DiscoveredBuiltin {
		discoveredMap[strings.ToLower(d.ID)] = d
	}

	existingMap := make(map[string]config.AgentConfig)
	for _, ag := range currentAgents {
		existingMap[strings.ToLower(ag.ID)] = ag
	}

	var defaultIndices []string
	for i, def := range builtinDefs {
		num := i + 1
		idxStr := strconv.Itoa(num)
		statusMark := "✗ 未发现"
		if existing, ok := existingMap[strings.ToLower(def.ID)]; ok {
			if existing.IsEnabled() {
				if existing.Path != "" {
					statusMark = fmt.Sprintf("✓ 已配置: %s", existing.Path)
				} else if disc, okDisc := discoveredMap[strings.ToLower(def.ID)]; okDisc && disc.Path != "" {
					statusMark = fmt.Sprintf("✓ 已配置 (发现: %s)", disc.Path)
				} else {
					statusMark = "✓ 已配置"
				}
				defaultIndices = append(defaultIndices, idxStr)
			} else {
				if existing.Path != "" {
					statusMark = fmt.Sprintf("- 已配置(禁用): %s", existing.Path)
				} else {
					statusMark = "- 已配置(禁用)"
				}
			}
		} else if disc, ok := discoveredMap[strings.ToLower(def.ID)]; ok {
			statusMark = fmt.Sprintf("✓ 已发现: %s", disc.Path)
			defaultIndices = append(defaultIndices, idxStr)
		}
		fmt.Fprintf(out, "  %2d) [%-20s] %-10s (%s)\n", num, statusMark, def.ID, def.Name)
	}
	fmt.Fprintln(out)

	defaultSelectStr := strings.Join(defaultIndices, ",")
	if defaultSelectStr == "" {
		if len(currentAgents) > 0 {
			defaultSelectStr = "none"
		} else {
			defaultSelectStr = "1,2"
		}
	}

	fmt.Fprintln(out, "请勾选要启用的预置引擎序号 (多个用逗号隔开，如 '1,2'；'all' 全部，'none' 留空)")
	selectInput := promptLine(reader, out, "勾选预置引擎", defaultSelectStr)

	var selectedDefs []config.BuiltinEngineDef
	selectInput = strings.TrimSpace(selectInput)
	if strings.EqualFold(selectInput, "all") {
		selectedDefs = builtinDefs
	} else if strings.EqualFold(selectInput, "none") {
		selectedDefs = nil
	} else {
		selectedIndices := make(map[int]bool)
		for _, part := range strings.Split(selectInput, ",") {
			part = strings.TrimSpace(part)
			if n, err := strconv.Atoi(part); err == nil && n >= 1 && n <= len(builtinDefs) {
				selectedIndices[n] = true
			}
		}
		for i, def := range builtinDefs {
			if selectedIndices[i+1] {
				selectedDefs = append(selectedDefs, def)
			}
		}
	}

	// 3. Item-by-item confirmation
	fmt.Fprintln(out, "\n[步骤 3/4] 对勾选的预置引擎逐项确认 (预填扫描路径与别名)")
	editedAgents := make(map[string]config.AgentConfig)
	for _, def := range selectedDefs {
		fmt.Fprintf(out, "\n[确认预置引擎: %s (%s)]\n", def.ID, def.Name)
		defaultEnabled := true
		if ex, ok := existingMap[strings.ToLower(def.ID)]; ok && ex.Enabled != nil {
			defaultEnabled = *ex.Enabled
		}
		enabled := promptBool(reader, out, fmt.Sprintf("  是否启用 %s", def.ID), defaultEnabled)

		var path string
		var aliases []string
		if enabled {
			defaultPath := ""
			if ex, ok := existingMap[strings.ToLower(def.ID)]; ok && ex.Path != "" {
				defaultPath = ex.Path
			} else if disc, ok := discoveredMap[strings.ToLower(def.ID)]; ok && disc.Path != "" {
				defaultPath = disc.Path
			}
			path = promptLine(reader, out, "  可执行文件路径 (留空自动搜索 PATH)", defaultPath)

			defaultAliases := strings.Join(def.DefaultAlias, ", ")
			if ex, ok := existingMap[strings.ToLower(def.ID)]; ok && len(ex.Alias) > 0 {
				defaultAliases = strings.Join(ex.Alias, ", ")
			}
			aliasStr := promptLine(reader, out, "  命令别名 (多个别名用逗号隔开)", defaultAliases)
			for _, a := range strings.Split(aliasStr, ",") {
				trimmed := strings.TrimSpace(a)
				if trimmed != "" {
					aliases = append(aliases, trimmed)
				}
			}
		} else {
			if ex, ok := existingMap[strings.ToLower(def.ID)]; ok {
				path = ex.Path
				aliases = ex.Alias
			}
		}
		editedAgents[strings.ToLower(def.ID)] = config.AgentConfig{
			ID:      def.ID,
			Path:    path,
			Alias:   aliases,
			Enabled: &enabled,
			Custom:  false,
		}
	}

	// 4. Custom CLI append
	fmt.Fprintln(out, "\n[步骤 4/4] 追加自定义 CLI (可选)")
	fmt.Fprintln(out, "注意: 自定义 CLI 需手动指定路径与别名，支持通用命令行执行；预置候选引擎已包含已知安装路径与别名探测规则。")
	fmt.Fprintln(out, "      内置预置与自定义分库存放，自定义 CLI 勿使用系统预置引擎 ID。")

	var unconfiguredCustom []config.DiscoveredAgent
	for _, c := range scanRes.DiscoveredCustom {
		if _, ok := existingMap[strings.ToLower(c.ID)]; !ok {
			unconfiguredCustom = append(unconfiguredCustom, c)
		}
	}
	if len(unconfiguredCustom) > 0 {
		var candNames []string
		for _, c := range unconfiguredCustom {
			candNames = append(candNames, fmt.Sprintf("%s (%s)", c.ID, c.Path))
		}
		fmt.Fprintf(out, "扫描到其他可能的终端 CLI: %s\n", strings.Join(candNames, "; "))
	}

	var newCustomAgents []config.AgentConfig
	for {
		addCustom := promptBool(reader, out, "是否追加自定义本地 Agent CLI？", false)
		if !addCustom {
			break
		}
		customID := promptLine(reader, out, "  自定义 Agent ID (如 myagent)", "")
		customID = strings.TrimSpace(customID)
		if customID == "" {
			break
		}
		if config.IsBuiltinEngine(customID) {
			fmt.Fprintf(out, "  [!] 错误: %q 为系统预置引擎，不可作为自定义 CLI 追加。请在步骤 2 内置引擎中配置。\n", customID)
			continue
		}

		lowerCustomID := strings.ToLower(customID)
		isDup := false
		if _, ok := existingMap[lowerCustomID]; ok {
			isDup = true
		}
		if !isDup {
			if _, ok := editedAgents[lowerCustomID]; ok {
				isDup = true
			}
		}
		if !isDup {
			for _, ag := range newCustomAgents {
				if strings.ToLower(ag.ID) == lowerCustomID {
					isDup = true
					break
				}
			}
		}
		if isDup {
			fmt.Fprintf(out, "  [!] 提示: Agent ID %q 已在列表中，请勿重复添加。\n", customID)
			continue
		}

		defaultPath := ""
		for _, c := range scanRes.DiscoveredCustom {
			if strings.EqualFold(c.ID, customID) {
				defaultPath = c.Path
				break
			}
		}
		path := promptLine(reader, out, "  可执行文件路径 (留空搜索 PATH)", defaultPath)
		aliasStr := promptLine(reader, out, "  命令别名 (逗号分隔)", customID)
		var aliases []string
		for _, a := range strings.Split(aliasStr, ",") {
			trimmed := strings.TrimSpace(a)
			if trimmed != "" {
				aliases = append(aliases, trimmed)
			}
		}
		enabled := promptBool(reader, out, fmt.Sprintf("  是否启用 %s", customID), true)
		cAgent := config.AgentConfig{
			ID:      customID,
			Path:    path,
			Alias:   aliases,
			Enabled: &enabled,
			Custom:  true,
		}
		newCustomAgents = append(newCustomAgents, cAgent)
		editedAgents[lowerCustomID] = cAgent
		fmt.Fprintf(out, "  [✓] 已添加自定义 Agent: %s (custom: true)\n", customID)
	}

	var finalAgents []config.AgentConfig
	seen := make(map[string]bool)

	// 1. Process current agents in their original order
	// Preserves untouched items (both built-in and custom) with all their attributes intact!
	for _, ag := range currentAgents {
		lowerID := strings.ToLower(ag.ID)
		if seen[lowerID] {
			continue
		}
		seen[lowerID] = true
		if updated, ok := editedAgents[lowerID]; ok {
			finalAgents = append(finalAgents, updated)
		} else {
			finalAgents = append(finalAgents, ag)
		}
	}

	// 2. Append newly selected built-in engines that were not previously configured
	for _, def := range selectedDefs {
		lowerID := strings.ToLower(def.ID)
		if !seen[lowerID] {
			seen[lowerID] = true
			if updated, ok := editedAgents[lowerID]; ok {
				finalAgents = append(finalAgents, updated)
			}
		}
	}

	// 3. Append newly added custom agents
	for _, c := range newCustomAgents {
		lowerID := strings.ToLower(c.ID)
		if !seen[lowerID] {
			seen[lowerID] = true
			finalAgents = append(finalAgents, c)
		}
	}

	fmt.Fprintln(out, "\n[✓] 本机 Agent 配置已更新。")
	fmt.Fprintln(out)
	return finalAgents
}

func printScanSummary(out io.Writer, res config.ScanResult) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "--- 扫描结果 ---")
	if len(res.DiscoveredBuiltin) > 0 {
		fmt.Fprintln(out, "  [已发现的预置引擎]:")
		for _, d := range res.DiscoveredBuiltin {
			fmt.Fprintf(out, "    ✓ %-10s : %s (%s)\n", d.ID, d.Path, d.Name)
		}
	} else {
		fmt.Fprintln(out, "  [预置引擎]: 未在默认路径中直接探测到预置引擎")
	}

	if len(res.MissingBuiltin) > 0 {
		var missingIDs []string
		for _, m := range res.MissingBuiltin {
			missingIDs = append(missingIDs, m.ID)
		}
		fmt.Fprintf(out, "  [未发现的预置引擎]: %s\n", strings.Join(missingIDs, ", "))
	}

	if len(res.IDEPlugins) > 0 {
		fmt.Fprintln(out, "  [IDE 插件与扩展提示]:")
		for _, p := range res.IDEPlugins {
			fmt.Fprintf(out, "    ℹ %s: %s\n", p.Name, p.Detail)
		}
	}

	if len(res.DiscoveredCustom) > 0 {
		fmt.Fprintln(out, "  [其他扫描到的 CLI (可作为自定义引擎追加)]: ")
		for _, c := range res.DiscoveredCustom {
			fmt.Fprintf(out, "    • %-10s : %s\n", c.ID, c.Path)
		}
	}
	fmt.Fprintln(out, "----------------")
	fmt.Fprintln(out)
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
		customTag := ""
		if p.Custom {
			customTag = " [自定义]"
		}
		if !p.Ok && strings.Contains(p.Detail, "禁用") {
			fmt.Fprintf(out, "  [-] %-10s [别名: %-14s]%s : %s\n", p.ID, aliases, customTag, p.Detail)
		} else if p.Ok {
			fmt.Fprintf(out, "  [✓] %-10s [别名: %-14s]%s : 可用 (%s)\n", p.ID, aliases, customTag, p.Detail)
		} else {
			fmt.Fprintf(out, "  [✗] %-10s [别名: %-14s]%s : %s\n", p.ID, aliases, customTag, p.Detail)
			allAgentsOk = false
		}
		announceAgents = append(announceAgents, model.AgentAnnounce{
			ID:     p.ID,
			Alias:  p.Alias,
			Ok:     p.Ok,
			Detail: p.Detail,
			Custom: p.Custom,
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
