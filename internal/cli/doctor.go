package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/autostart"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "运行本机诊断与全网机器及 CLI 拓扑探测",
		Long: `对当前终端环境、配置文件、MQTT Broker 连通性以及本机 Agent CLI 进行全面探测。
随后同步最新 Announce 广播，并拉取全网设备汇总为扁平表格。`,
		Run: func(cmd *cobra.Command, args []string) {
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "========================================")
			fmt.Fprintln(out, "AgentPulse 诊断报告 (Doctor Diagnostics)")
			fmt.Fprintln(out, "========================================")

			// 1. 本机运行环境
			fmt.Fprintf(out, "[OK] 运行环境: Go %s (%s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

			// 2. 终端能力
			if termutil.IsTerminal(os.Stdout) {
				w, h, err := term.GetSize(int(os.Stdout.Fd()))
				if err == nil {
					fmt.Fprintf(out, "[OK] 交互终端: 检测到 TTY (%dx%d)\n", w, h)
				} else {
					fmt.Fprintln(out, "[OK] 交互终端: 检测到 TTY")
				}
			} else {
				fmt.Fprintln(out, "[INFO] 终端模式: 非交互式管道 / 纯文本模式")
			}

			// 3. 配置文件探测
			fileCfg, confPath, cfgErr := loadActiveConfig(cmd)
			if cfgErr != nil {
				if os.IsNotExist(cfgErr) {
					fmt.Fprintf(out, "[WARN] 配置文件: %s 不存在 (可运行 'agentpulse config' 初始化)\n", confPath)
				} else {
					fmt.Fprintf(out, "[ERROR] 配置文件解析失败: %v\n", cfgErr)
				}
			} else {
				info, err := os.Stat(confPath)
				permStr := ""
				if err == nil {
					permStr = fmt.Sprintf("权限: %#o", info.Mode().Perm())
				}
				concurrencyStr := "1 (串行默认)"
				if fileCfg.MaxConcurrentJobs > 1 {
					concurrencyStr = fmt.Sprintf("%d (并发)", fileCfg.MaxConcurrentJobs)
				}
				if fileCfg.MachineID == "" {
					fmt.Fprintf(out, "[WARN] 配置文件: %s (%s, 未配置 machine_id，请运行 'agentpulse config' 配置)\n", confPath, permStr)
				} else {
					fmt.Fprintf(out, "[OK] 配置文件: %s (%s, machine_id=%s, rev=%d, 并发上限=%s)\n", confPath, permStr, fileCfg.MachineID, fileCfg.Rev, concurrencyStr)
				}
				if fileCfg.Autostart {
					fmt.Fprintf(out, "[OK] 开机自启 (LaunchAgent): 已在配置中启用\n")
				} else {
					fmt.Fprintf(out, "[INFO] 开机自启 (LaunchAgent): 未启用 (可通过 'agentpulse config' 启用)\n")
				}
				if autostart.IsSupported() {
					st := autostart.Status()
					if st.Running {
						fmt.Fprintf(out, "[OK] 常驻守护进程: 运行中 (%s)\n", st.Detail)
					} else if st.Installed {
						fmt.Fprintf(out, "[WARN] 常驻守护进程: 已安装但未运行 (%s)\n", st.Detail)
					}
				}
			}

			// 4. 本机 Agent CLI 探测
			fmt.Fprintln(out, "\n[本机 Agent CLI 探测]:")
			var agentsToProbe []config.AgentConfig
			if fileCfg != nil && len(fileCfg.Agents) > 0 {
				agentsToProbe = fileCfg.Agents
			} else {
				agentsToProbe = config.DefaultAgents()
			}

			probedStatuses := config.ProbeAgents(agentsToProbe)
			var announceAgents []model.AgentAnnounce
			for _, st := range probedStatuses {
				aliases := strings.Join(st.Alias, ", ")
				if aliases == "" {
					aliases = st.ID
				}
				if !st.Ok && strings.Contains(st.Detail, "禁用") {
					fmt.Fprintf(out, "  [-] %-10s [别名: %-14s] : %s\n", st.ID, aliases, st.Detail)
				} else if st.Ok {
					fmt.Fprintf(out, "  [✓] %-10s [别名: %-14s] : %s\n", st.ID, aliases, st.Detail)
				} else {
					fmt.Fprintf(out, "  [✗] %-10s [别名: %-14s] : %s\n", st.ID, aliases, st.Detail)
				}
				announceAgents = append(announceAgents, model.AgentAnnounce{
					ID:     st.ID,
					Alias:  st.Alias,
					Ok:     st.Ok,
					Detail: st.Detail,
				})
			}

			// 5. Broker 连通性测试 & doctor 后发 Announce
			fmt.Fprintln(out, "\n[数据层与 Broker 探测]:")
			var brokerOnline bool
			mqttCfg := resolveMQTTConfig(cmd)

			if mqttCfg.BrokerURL == "" {
				fmt.Fprintln(out, "  [WARN] 未配置 MQTT Broker URL。请运行 'agentpulse config' 初始化配置，或通过 --mqtt-url 指定")
			} else {
				authInfo := "anonymous"
				if mqttCfg.Username != "" {
					authInfo = fmt.Sprintf(`username %q`, mqttCfg.Username)
				}
				fmt.Fprintf(out, "  [OK] 数据存储: MQTT (%s, auth=%s, settle=%v)\n", mqttCfg.BrokerURL, authInfo, mqttCfg.SettleTimeout)
				if mqttCfg.IsTLSEnabled() {
					tlsParts := []string{"enabled"}
					if mqttCfg.InsecureSkipVerify {
						tlsParts = append(tlsParts, "insecure=true")
					}
					if mqttCfg.CACertFile != "" {
						tlsParts = append(tlsParts, fmt.Sprintf("ca=%s", mqttCfg.CACertFile))
					}
					if mqttCfg.ClientCertFile != "" {
						tlsParts = append(tlsParts, "mTLS=true")
					}
					if mqttCfg.ServerName != "" {
						tlsParts = append(tlsParts, fmt.Sprintf("sni=%s", mqttCfg.ServerName))
					}
					fmt.Fprintf(out, "  [INFO] MQTT TLS: %s\n", strings.Join(tlsParts, ", "))
				}

				fmt.Fprintf(out, "  [...] Broker 连通性测试中 (目标: %s)...\n", mqttCfg.BrokerURL)
				connTimeout := mqttCfg.Timeout
				if connTimeout <= 0 {
					connTimeout = 10 * time.Second
				}
				testErr := testMQTTConnection(mqttCfg, connTimeout)
				if testErr != nil {
					fmt.Fprintf(out, "  [WARN] Broker 测连失败: %v\n", testErr)
				} else {
					fmt.Fprintln(out, "  [OK] Broker 连接正常")
					brokerOnline = true
				}
			}

			// Doctor 后发 announce
			if brokerOnline && fileCfg != nil && fileCfg.MachineID != "" {
				host, _ := os.Hostname()
				ann := model.MachineAnnounce{
					V:         1,
					MachineID: fileCfg.MachineID,
					TS:        time.Now().UTC(),
					Rev:       fileCfg.Rev,
					Hostname:  host,
					OS:        fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
					Agents:    announceAgents,
				}
				opts := paho.NewClientOptions()
				doctorAnnounceCfg := mqttCfg
				if doctorAnnounceCfg.ClientID != "" {
					doctorAnnounceCfg.ClientID += "-announce"
				}
				if doctorAnnounceCfg.Timeout <= 0 {
					doctorAnnounceCfg.Timeout = 10 * time.Second
				}
				_ = mqtt.ApplyClientOptions(opts, doctorAnnounceCfg)
				c := paho.NewClient(opts)
				if token := c.Connect(); token.WaitTimeout(doctorAnnounceCfg.Timeout) && token.Error() == nil {
					if err := mqtt.PublishAnnounce(c, ann); err == nil {
						fmt.Fprintf(out, "  [✓] 已同步最新 Announce 广播至 Broker (v1, rev %d, %d 个 CLI)\n", ann.Rev, len(ann.Agents))
					}
					c.Disconnect(100)
				}
			}

			// 6. 全网机 × CLI 扁平表格
			fmt.Fprintln(out, "\n================================================================================")
			fmt.Fprintln(out, "全网节点与 CLI 拓扑 (全网扁平表)")
			fmt.Fprintln(out, "================================================================================")

			st, storeErr := getStore(cmd)
			if storeErr != nil {
				fmt.Fprintf(out, "[ERROR] 无法查询全网拓扑: %v\n", storeErr)
			} else {
				defer st.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()

				machines, _ := st.ListMachines(ctx)
				announces, _ := st.ListAnnounces(ctx)

				annMap := make(map[string]model.MachineAnnounce)
				for _, a := range announces {
					annMap[a.MachineID] = a
				}

				// Build rows for all known nodes
				type nodeRow struct {
					ID       string
					Hostname string
					OS       string
					Status   string
					Agents   string
				}
				var rows []nodeRow

				// Process machines from store
				seen := make(map[string]bool)
				for _, m := range machines {
					seen[m.ID] = true
					statusStr := "OFFLINE"
					if m.Online {
						statusStr = fmt.Sprintf("ONLINE (%s)", m.HeartbeatAgo())
					}

					ann, hasAnn := annMap[m.ID]
					hostname := m.Name
					osStr := m.Platform
					agentsStr := "—"
					if hasAnn {
						if ann.Hostname != "" {
							hostname = ann.Hostname
						}
						if ann.OS != "" {
							osStr = ann.OS
						}
						var agList []string
						for _, ag := range ann.Agents {
							sym := "✗"
							if ag.Ok {
								sym = "✓"
							}
							agList = append(agList, fmt.Sprintf("%s:%s", ag.ID, sym))
						}
						if len(agList) > 0 {
							agentsStr = strings.Join(agList, " ")
						}
					}
					rows = append(rows, nodeRow{
						ID:       m.ID,
						Hostname: hostname,
						OS:       osStr,
						Status:   statusStr,
						Agents:   agentsStr,
					})
				}

				// If there are announces without a machine record
				for _, ann := range announces {
					if !seen[ann.MachineID] {
						var agList []string
						for _, ag := range ann.Agents {
							sym := "✗"
							if ag.Ok {
								sym = "✓"
							}
							agList = append(agList, fmt.Sprintf("%s:%s", ag.ID, sym))
						}
						agentsStr := strings.Join(agList, " ")
						rows = append(rows, nodeRow{
							ID:       ann.MachineID,
							Hostname: ann.Hostname,
							OS:       ann.OS,
							Status:   "REGISTERED",
							Agents:   agentsStr,
						})
					}
				}

				if len(rows) == 0 {
					fmt.Fprintln(out, "当前网内无活跃节点。")
				} else {
					fmt.Fprintf(out, "%-16s %-18s %-14s %-20s %s\n", "机器 (MACHINE)", "主机名 (HOST)", "系统 (OS)", "状态 (STATUS)", "CLI 支持 (AGENTS)")
					fmt.Fprintln(out, strings.Repeat("-", 80))
					for _, r := range rows {
						fmt.Fprintf(out, "%-16s %-18s %-14s %-20s %s\n", r.ID, r.Hostname, r.OS, r.Status, r.Agents)
					}
				}
			}

			fmt.Fprintln(out, "================================================================================")
			fmt.Fprintln(out, "诊断完成。")
		},
	}
}
