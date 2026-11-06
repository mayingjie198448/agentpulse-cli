package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/mayingjie198448/agentpulse-cli/internal/tui"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

var (
	plainOutput bool
)

// defaultDangerouslySkipPermissions is the dual-role daemon's opt-in.
// It stays false so this process does not add --dangerously-skip-permissions.
const defaultDangerouslySkipPermissions = false

// NewRootCmd constructs the root command for agentpulse.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "agentpulse",
		Short: "跨机器 AI Agent 协同与全网监控 CLI",
		Long: `AgentPulse 监控并调度多台机器上的自主 AI Agent（Google Antigravity / agy 及 OpenCode）。

常驻单进程双角色模式：
  直接执行 'agentpulse'（无子命令）时，将启动常驻守护服务：
  1. 本机边缘节点角色：上报本机心跳、Announce 拓扑广播，并在后台监听与执行远程调度任务；
  2. 全网监控大盘角色：订阅全网节点与任务流式事件，在终端展示实时监控看板（非 TTY 自动退化为纯文本快照）。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDualRoleDaemon(cmd, args)
		},
	}

	rootCmd.PersistentFlags().BoolVar(&plainOutput, "plain", false, "强制纯文本输出模式 (禁用交互式 TUI)")
	registerStoreFlags(rootCmd)

	// 核心对外子命令 (产品边界: config/doctor/dispatch/jobs/watch/version)
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newDispatchCmd())
	rootCmd.AddCommand(newJobsCmd())
	rootCmd.AddCommand(newWatchCmd())
	rootCmd.AddCommand(newVersionCmd())
	safetyCmd := newSafetyCmd()
	safetyCmd.Hidden = true
	rootCmd.AddCommand(safetyCmd)

	// listen/wrap 保留为隐藏兼容命令
	listenCmd := newListenCmd()
	listenCmd.Hidden = true
	rootCmd.AddCommand(listenCmd)

	wrapCmd := newWrapCmd()
	wrapCmd.Hidden = true
	rootCmd.AddCommand(wrapCmd)

	autostartCmd := newAutostartCmd()
	autostartCmd.Hidden = true
	rootCmd.AddCommand(autostartCmd)

	listCmd := newListCmd()
	listCmd.Hidden = true
	rootCmd.AddCommand(listCmd)

	logsCmd := newLogsCmd()
	logsCmd.Hidden = true
	rootCmd.AddCommand(logsCmd)

	statusCmd := newStatusCmd()
	statusCmd.Hidden = true
	rootCmd.AddCommand(statusCmd)

	return rootCmd
}

// runDualRoleDaemon starts the integrated heartbeat/announce/listener and watch monitor.
func runDualRoleDaemon(cmd *cobra.Command, args []string) error {
	// Broker 只来自 config.yaml 与显式 CLI 标志。没有 Broker 时直接报错，不再进入内存模拟。
	mqttCfg := resolveMQTTConfig(cmd)
	if mqttCfg.BrokerURL == "" {
		return fmt.Errorf("未配置 MQTT Broker URL。请运行 'agentpulse config' 初始化配置，或通过 --mqtt-url 指定")
	}

	baseClientID := mqttCfg.ClientID
	if baseClientID == "" {
		baseClientID = fmt.Sprintf("agentpulse-%d", time.Now().UnixNano())
	}

	listenerMQTT := mqttCfg
	listenerMQTT.ClientID = baseClientID + "-listener"

	announceMQTT := mqttCfg
	announceMQTT.ClientID = baseClientID + "-announce"

	storeMQTT := mqttCfg
	storeMQTT.ClientID = baseClientID + "-store"

	fileCfg, _, _ := loadActiveConfig(cmd)
	machineID := ""
	if fileCfg != nil && fileCfg.MachineID != "" {
		machineID = fileCfg.MachineID
	}
	if machineID == "" {
		host, _ := os.Hostname()
		machineID = wrapper.SanitizeID(host)
	}
	if machineID == "" {
		machineID = "machine-1"
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// 构造本机 Announce 拓扑信息
	var agentsToProbe []config.AgentConfig
	if fileCfg != nil && len(fileCfg.Agents) > 0 {
		agentsToProbe = fileCfg.Agents
	} else {
		agentsToProbe = config.DefaultAgents()
	}
	probedStatuses := config.ProbeAgents(agentsToProbe)
	var announceAgents []model.AgentAnnounce
	for _, st := range probedStatuses {
		announceAgents = append(announceAgents, model.AgentAnnounce{
			ID:     st.ID,
			Alias:  st.Alias,
			Ok:     st.Ok,
			Detail: st.Detail,
		})
	}
	host, _ := os.Hostname()
	rev := 1
	if fileCfg != nil && fileCfg.Rev > 0 {
		rev = fileCfg.Rev
	}
	ann := model.MachineAnnounce{
		V:         1,
		MachineID: machineID,
		TS:        time.Now().UTC(),
		Rev:       rev,
		Hostname:  host,
		OS:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Agents:    announceAgents,
	}

	// 角色 1: 本机 Listener + 心跳守护进程.
	// This revision previously forced DangerouslySkipPermissions on.
	// Other historical SHAs left the listen/wrap BoolVar default false.
	// The daemon does not opt in. An explicit flag still has to pass
	// safety.Prepare; isolation failure does not start a bare child.
	pMQTT := &listenerMQTT
	isPlain := termutil.ShouldUsePlain(plainOutput)
	listenerCfg := wrapper.ListenerConfig{
		MachineID:                  machineID,
		MachineName:                host,
		HeartbeatInterval:          mqttCfg.HeartbeatCheckInterval,
		StatusFile:                 wrapper.DefaultStatusFile,
		MQTT:                       pMQTT,
		DangerouslySkipPermissions: defaultDangerouslySkipPermissions,
		Quiet:                      !isPlain,
		Stdout:                     io.Discard,
		Stderr:                     io.Discard,
		OnMQTTConnected: func() {
			opts := paho.NewClientOptions()
			_ = mqtt.ApplyClientOptions(opts, announceMQTT)
			c := paho.NewClient(opts)
			waitTimeout := announceMQTT.Timeout
			if waitTimeout <= 0 {
				waitTimeout = 10 * time.Second
			}
			if token := c.Connect(); token.WaitTimeout(waitTimeout) && token.Error() == nil {
				_ = mqtt.PublishAnnounce(c, ann)
				c.Disconnect(100)
			}
		},
	}
	if isPlain {
		listenerCfg.Stdout = cmd.OutOrStdout()
		listenerCfg.Stderr = cmd.ErrOrStderr()
	}

	l, err := wrapper.NewListener(listenerCfg)
	if err != nil {
		return fmt.Errorf("初始化本机监听服务失败: %w", err)
	}

	go func() {
		_ = l.Run(ctx)
	}()

	// 角色 2: 全网进度大盘订阅与渲染
	st, err := mqtt.New(storeMQTT)
	if err != nil {
		return fmt.Errorf("连接 MQTT 监控大盘失败: %w", err)
	}
	defer st.Close()

	if isPlain {
		return runPlainWatch(ctx, st, 2*time.Second)
	}

	p := tea.NewProgram(tui.NewModel(st), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}

// Execute runs the root command.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
