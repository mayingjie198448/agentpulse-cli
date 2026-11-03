package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/mayingjie198448/agentpulse-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newWatchCmd() *cobra.Command {
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "watch [job]",
		Short: "实时监控任务大盘或追踪指定任务流式日志",
		Long: `启动实时监控面板：
  • 若未提供 [job] 参数：启动 Cyber 风格交互式 TUI 监控大盘（展示全网节点心跳、任务进度与实时日志）；
    在非 TTY 管道或传入 --plain 参数时，自动退化为周期性纯文本快照。
  • 若提供 [job] 参数：定向追踪该任务的实时进度演进与控制台日志输出流，直至任务结束。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			// If specific job specified
			if len(args) > 0 {
				jobID := args[0]
				return watchSingleJob(cmd.Context(), st, jobID)
			}

			if termutil.ShouldUsePlain(plainOutput) {
				return runPlainWatch(cmd.Context(), st, interval)
			}

			// Interactive TUI mode with Bubble Tea
			p := tea.NewProgram(tui.NewModel(st), tea.WithAltScreen(), tea.WithMouseCellMotion())
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().DurationVarP(&interval, "interval", "i", 2*time.Second, "纯文本快照刷新间隔")
	return cmd
}

// watchSingleJob streams status and log events for a specific job.
func watchSingleJob(ctx context.Context, st store.Store, jobID string) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	out := os.Stdout

	fmt.Fprintf(out, "=== 正在监听任务: %s ===\n", jobID)

	// Fetch current job status
	initialJob, err := st.GetJob(ctx, jobID)
	if err == nil && initialJob != nil {
		fmt.Fprintf(out, "[状态] 节点: %s | 引擎: %s | 状态: %s | 进度: %.1f%% | 标题: %s\n",
			initialJob.MachineID, initialJob.Engine, initialJob.Status, initialJob.Progress, initialJob.Title)
		if initialJob.Status == model.StatusDone || initialJob.Status == model.StatusFailed {
			fmt.Fprintf(out, "[完成] 任务已结束，最终状态: %s\n", initialJob.Status)
			return nil
		}
	}

	// Fetch and display existing logs
	historicalLogs, _ := st.GetLogs(ctx, jobID, 50)
	if len(historicalLogs) > 0 {
		fmt.Fprintf(out, "--- 最近 %d 条日志 ---\n", len(historicalLogs))
		for _, l := range historicalLogs {
			fmt.Fprintf(out, "[%s] [%-5s] %s\n", l.Timestamp.Format("15:04:05"), l.Level, l.Message)
		}
		fmt.Fprintln(out, "--- 实时日志流 ---")
	}

	evCh, err := st.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("failed to subscribe to events: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			fmt.Fprintln(out, "\n退出任务监听。")
			return nil
		case ev, ok := <-evCh:
			if !ok {
				return nil
			}
			switch ev.Type {
			case model.EventJobUpdated:
				if ev.Job != nil && ev.Job.ID == jobID {
					fmt.Fprintf(out, "[进度更新] %5.1f%% [%s]: %s\n", ev.Job.Progress, ev.Job.Status, ev.Job.Title)
					if ev.Job.Status == model.StatusDone || ev.Job.Status == model.StatusFailed {
						fmt.Fprintf(out, "[任务结束] 最终状态: %s (时长: %s)\n", ev.Job.Status, ev.Job.DurationStr)
						return nil
					}
				}
			case model.EventLogAdded:
				if ev.Log != nil && ev.Log.JobID == jobID {
					fmt.Fprintf(out, "[%s] [%-5s] %s\n", ev.Log.Timestamp.Format("15:04:05"), ev.Log.Level, ev.Log.Message)
				}
			}
		}
	}
}

// runPlainWatch prints periodic status snapshots in non-interactive/piped environments
func runPlainWatch(ctx context.Context, st store.Store, interval time.Duration) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	printSnapshot := func() {
		machines, _ := st.ListMachines(ctx)
		jobs, _ := st.ListJobs(ctx)

		fmt.Printf("=== [AgentPulse 监控快照 @ %s] ===\n", time.Now().Format("15:04:05"))
		fmt.Printf("节点设备 (%d 在线 / %d 总计):\n", countOnline(machines), len(machines))
		for _, m := range machines {
			status := "离线"
			if m.Online {
				status = "在线"
			}
			fmt.Printf("  - %-12s [%-4s] 心跳: %-8s 任务数: %d\n", m.Name, status, m.HeartbeatAgo(), m.JobCount)
		}

		fmt.Printf("任务列表 (%d total):\n", len(jobs))
		for _, j := range jobs {
			fmt.Printf("  - %-14s [%-8s] @%-10s [%-7s] %5.1f%% : %s\n",
				j.ID, j.Engine, j.MachineID, j.Status, j.Progress, j.Title)
		}
		fmt.Println()
	}

	// Print first snapshot immediately
	printSnapshot()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			fmt.Println("\n退出监控...")
			return nil
		case <-ticker.C:
			printSnapshot()
		}
	}
}

func countOnline(machines []model.Machine) int {
	cnt := 0
	for _, m := range machines {
		if m.Online {
			cnt++
		}
	}
	return cnt
}
