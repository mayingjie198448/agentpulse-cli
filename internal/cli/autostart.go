package cli

import (
	"fmt"

	"github.com/mayingjie198448/agentpulse-cli/internal/autostart"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/spf13/cobra"
)

func newAutostartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autostart [status|enable|disable|restart]",
		Short: "管理本机开机自启常驻后台守护进程 (macOS LaunchAgent)",
		Long: `查询或管理 AgentPulse 后台常驻守护进程的开机自启状态。
在 macOS 上通过当前用户级 LaunchAgent (~/Library/LaunchAgents/com.agentpulse.daemon.plist) 实现，
无需 sudo 权限。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := "status"
			if len(args) > 0 {
				action = args[0]
			}

			out := cmd.OutOrStdout()
			fileCfg, confPath, _ := loadActiveConfig(cmd)
			if fileCfg == nil {
				fileCfg = &config.Config{
					Version: 1,
					Rev:     1,
				}
			}

			switch action {
			case "status":
				st := autostart.Status()
				fmt.Fprintln(out, "========================================")
				fmt.Fprintln(out, "AgentPulse 开机自启常驻状态")
				fmt.Fprintln(out, "========================================")
				cfgStatus := "未启用"
				if fileCfg.Autostart {
					cfgStatus = "已启用"
				}
				fmt.Fprintf(out, "配置文件配置: %s (文件: %s)\n", cfgStatus, confPath)
				fmt.Fprintf(out, "平台支持状态: %v\n", st.Supported)
				fmt.Fprintf(out, "LaunchAgent : %s\n", st.Detail)
				if st.PlistPath != "" {
					fmt.Fprintf(out, "Plist 文件路径: %s\n", st.PlistPath)
				}
				return nil

			case "enable":
				if !autostart.IsSupported() {
					return fmt.Errorf("当前平台暂不支持开机自启常驻后台 (目前支持 macOS LaunchAgent)")
				}
				fileCfg.Autostart = true
				if err := config.SaveConfig(confPath, fileCfg); err != nil {
					return fmt.Errorf("保存配置文件失败: %w", err)
				}
				if err := autostart.Enable(fileCfg, ""); err != nil {
					return fmt.Errorf("启用 LaunchAgent 失败: %w", err)
				}
				fmt.Fprintln(out, "[✓] 已成功开启并加载开机自启守护进程 (LaunchAgent)")
				return nil

			case "disable":
				fileCfg.Autostart = false
				if err := config.SaveConfig(confPath, fileCfg); err != nil {
					return fmt.Errorf("保存配置文件失败: %w", err)
				}
				if err := autostart.Disable(); err != nil {
					return fmt.Errorf("禁用 LaunchAgent 失败: %w", err)
				}
				fmt.Fprintln(out, "[✓] 已成功停止并移除开机自启守护进程 (LaunchAgent)")
				return nil

			case "restart":
				if !autostart.IsSupported() {
					return fmt.Errorf("当前平台暂不支持开机自启常驻后台")
				}
				if err := autostart.Restart(fileCfg, fileCfg.MachineID, ""); err != nil {
					return fmt.Errorf("重启守护进程失败: %w", err)
				}
				fmt.Fprintln(out, "[✓] 已重启常驻守护进程")
				return nil

			default:
				return fmt.Errorf("未知指令 %q，支持: status, enable, disable, restart", action)
			}
		},
	}
	return cmd
}
