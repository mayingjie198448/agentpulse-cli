package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/dispatch"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func newDispatchCmd() *cobra.Command {
	var (
		machineFlag        string
		machineIDFlag      string
		jobID              string
		engine             string
		cwd                string
		title              string
		repoFlag           string
		projectFlag        string
		promptFlag         string
		timeoutSeconds     int
		waitTimeoutSeconds int
		requestedBy        string
		envFlags           []string
		commandFlag        string
		argFlags           []string
		waitFlag           bool
		followFlag         bool
		quiet              bool
		forceFlag          bool
	)

	dispatchCmd := &cobra.Command{
		Use:   "dispatch [--machine <id>] [--force] <别名|CLI> -- <参数…>",
		Short: "远程派发任务至指定机器或自动匹配在线节点",
		Long: `向 MQTT 消息中枢下发作业调度指令 (agentpulse/v1/machines/{machine_id}/commands/dispatch)。

调度门禁 (Dispatch Gate):
  默认情况下，dispatch 会进行双重在线门禁核验：
  1. 目标机器必须在线 (近 45 秒内心跳新鲜)。
  2. 目标机器广播的 Announce 中必须包含对应的 Agent CLI 且状态为就绪 (ok: true)。
  若省略 --machine，将自动扫描网内支持该 Agent 的唯一在线机器；发现多台时需显式指定；
  使用 --force 可强行绕过门禁下发。

示例:
  # 自动选机并派发 agy 任务:
  agentpulse dispatch agy -- run --prompt "修复数据库连接池"

  # 指定目标机器派发:
  agentpulse dispatch --machine mac-studio op -- -p "重构认证模块"

  # 派发并实时跟踪进度与输出日志:
  agentpulse dispatch --machine mac-studio --follow agy -- -p "运行测试集"

  # 强制派发给可能正在冷启动的机器:
  agentpulse dispatch --machine gpu-box --force agy -- -p "预热模型"`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			out := cmd.OutOrStdout()
			errOut := cmd.OutOrStderr()

			machineID := machineFlag
			if machineID == "" {
				machineID = machineIDFlag
			}
			if machineID != "" {
				machineID = wrapper.SanitizeID(machineID)
			}

			// Parse command & arguments
			var agentOrCLI string
			var finalArgs []string

			if len(rawArgs) > 0 {
				agentOrCLI = rawArgs[0]
				args := rawArgs[1:]
				if len(args) > 0 && args[0] == "--" {
					args = args[1:]
				}
				finalArgs = append(finalArgs, args...)
			} else if commandFlag != "" {
				agentOrCLI = commandFlag
			}
			if len(argFlags) > 0 {
				finalArgs = append(finalArgs, argFlags...)
			}
			if len(finalArgs) > 0 && finalArgs[0] == "--" {
				finalArgs = finalArgs[1:]
			}

			if strings.TrimSpace(agentOrCLI) == "" {
				return fmt.Errorf("必须指定要派发的 Agent 别名或 CLI 命令 (例如: agentpulse dispatch agy -- -p \"...\") (command to dispatch is required)")
			}

			// Resolve MQTT configuration
			mqttCfg := resolveMQTTConfig(cmd)
			if mqttCfg.BrokerURL == "" {
				return fmt.Errorf("派发任务需要配置 MQTT Broker (请先运行 'agentpulse config' 或传入 --mqtt-url) (broker URL is required)")
			}

			// Setup cancellation
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

			// If machineID is omitted and not forced, attempt auto-selection
			if machineID == "" && !forceFlag {
				st, err := getStore(cmd)
				if err == nil {
					defer st.Close()
					scanCtx, scanCancel := context.WithTimeout(ctx, 3*time.Second)
					defer scanCancel()

					machines, _ := st.ListMachines(scanCtx)
					announces, _ := st.ListAnnounces(scanCtx)
					annMap := make(map[string]model.MachineAnnounce)
					for _, a := range announces {
						annMap[a.MachineID] = a
					}

					var candidates []string
					for _, m := range machines {
						if m.Online {
							if ann, ok := annMap[m.ID]; ok {
								if ag, found := ann.FindAgent(agentOrCLI); found && ag.Ok {
									candidates = append(candidates, m.ID)
								}
							}
						}
					}

					if len(candidates) == 1 {
						machineID = candidates[0]
						if !quiet {
							fmt.Fprintf(out, "[agentpulse dispatch] 自动选定就绪机器: %s (支持 %s)\n", machineID, agentOrCLI)
						}
					} else if len(candidates) > 1 {
						return fmt.Errorf("网内有多台支持 %q 的在线机器: %v，请使用 --machine <id> 显式指定", agentOrCLI, candidates)
					} else {
						return fmt.Errorf("未在网内找到任何支持 %q 且当前在线的机器；请确认目标机器运行正常，或使用 --machine <id> --force 强制派发", agentOrCLI)
					}
				}
			}

			if machineID == "" {
				return fmt.Errorf("必须指定目标机器 (通过 --machine <id> 指定，或确保网内存在唯一匹配的在线机器) (machine ID is required)")
			}

			// Resolve engine
			var engType model.EngineType
			switch strings.ToLower(engine) {
			case "opencode":
				engType = model.EngineOpenCode
			case "agy":
				engType = model.EngineAgy
			default:
				if engine != "" {
					return fmt.Errorf("invalid engine %q: must be 'agy' or 'opencode'", engine)
				}
			}

			// Parse environment variables
			envMap := make(map[string]string)
			for _, e := range envFlags {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				} else {
					envMap[parts[0]] = ""
				}
			}

			pMQTT := &mqttCfg

			cfg := dispatch.Config{
				MachineID:          machineID,
				AgentName:          agentOrCLI,
				JobID:              jobID,
				Engine:             engType,
				Cwd:                cwd,
				Command:            agentOrCLI,
				Args:               finalArgs,
				Title:              title,
				Repo:               repoFlag,
				Project:            projectFlag,
				Prompt:             promptFlag,
				TimeoutSeconds:     timeoutSeconds,
				WaitTimeoutSeconds: waitTimeoutSeconds,
				Env:                envMap,
				RequestedBy:        requestedBy,
				Wait:               waitFlag,
				Follow:             followFlag,
				Quiet:              quiet,
				Force:              forceFlag,
				MQTT:               pMQTT,
				Out:                out,
				ErrOut:             errOut,
			}

			_, err := dispatch.Dispatch(ctx, cfg)
			return err
		},
	}

	dispatchCmd.Flags().SetInterspersed(false)

	dispatchCmd.Flags().StringVarP(&machineFlag, "machine", "m", "", "目标机器 ID (若省略则自动匹配网内就绪机器)")
	dispatchCmd.Flags().StringVar(&machineIDFlag, "machine-id", "", "目标机器 ID (等同于 --machine)")
	dispatchCmd.Flags().StringVar(&jobID, "job-id", "", "作业 ID (省略时自动生成)")
	dispatchCmd.Flags().StringVar(&engine, "engine", "", "Agent 引擎类型 ('agy' 或 'opencode')")
	dispatchCmd.Flags().StringVar(&cwd, "cwd", "", "远端工作目录")
	dispatchCmd.Flags().StringVar(&title, "title", "", "作业标题或说明")
	dispatchCmd.Flags().StringVar(&repoFlag, "repo", "", "仓库名 (如 org/repo，省略时自动推断)")
	dispatchCmd.Flags().StringVar(&projectFlag, "project", "", "项目名 (省略时自动推断)")
	dispatchCmd.Flags().StringVarP(&promptFlag, "prompt", "p", "", "任务提示词 (省略时从命令参数自动提取)")
	dispatchCmd.Flags().IntVar(&timeoutSeconds, "timeout-seconds", 0, "远端作业超时时间 (秒，0 表示不设限)")
	dispatchCmd.Flags().IntVar(&waitTimeoutSeconds, "wait-timeout", 0, "本地等待超时时间 (秒，0 表示不设限)")
	dispatchCmd.Flags().StringVar(&requestedBy, "requested-by", "", "派发者身份标识 (默认: $USER@hostname)")
	dispatchCmd.Flags().StringSliceVarP(&envFlags, "env", "e", nil, "透传环境变量 (key=value，可多次指定)")
	dispatchCmd.Flags().StringVar(&commandFlag, "command", "", "执行命令 (推荐直接作为首个参数传入)")
	dispatchCmd.Flags().StringSliceVar(&argFlags, "arg", nil, "命令参数 (可多次指定)")
	dispatchCmd.Flags().BoolVarP(&waitFlag, "wait", "w", false, "等待任务执行完成并报告进度")
	dispatchCmd.Flags().BoolVarP(&followFlag, "follow", "f", false, "等待任务执行完成并实时流式输出日志")
	dispatchCmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "静默模式，抑制横幅与状态转换提示")
	dispatchCmd.Flags().BoolVar(&forceFlag, "force", false, "强制派发，跳过目标机器在线与 CLI 状态门禁")

	return dispatchCmd
}
