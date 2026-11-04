package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newJobsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "查看全网任务列表及执行状态",
		Long: `从消息存储中检索并汇总跨机器的全部 AI Agent 任务（包含运行中、排队中、已完成与失败任务）。
支持格式化表格输出以及机器可读的 JSON 输出。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()

			jobs, err := st.ListJobs(ctx)
			if err != nil {
				return fmt.Errorf("failed to list jobs: %w", err)
			}

			out := cmd.OutOrStdout()

			if jsonOutput {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(jobs)
			}

			fmt.Fprintf(out, "=== AgentPulse 全网任务列表 (%d total) ===\n", len(jobs))
			if len(jobs) == 0 {
				fmt.Fprintln(out, "当前暂无活跃或历史任务记录。")
				return nil
			}

			fmt.Fprintf(out, "%-16s %-12s %-10s %-10s %-8s %-12s %s\n",
				"任务 ID", "节点", "引擎", "状态", "进度", "运行时长", "任务说明")
			fmt.Fprintln(out, strings.Repeat("-", 85))

			for _, j := range jobs {
				dur := j.DurationStr
				if dur == "" {
					dur = "-"
				}
				fmt.Fprintf(out, "%-16s %-12s %-10s %-10s %5.1f%%   %-12s %s\n",
					j.ID, j.MachineID, j.Engine, strings.ToUpper(string(j.Status)), j.Progress, dur, j.Title)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "以 JSON 格式输出任务列表")
	return cmd
}
