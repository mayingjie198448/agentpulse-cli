package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
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
	cmd.AddCommand(newJobsDeleteCmd())
	return cmd
}

func newJobsDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "delete <job-id>",
		Aliases: []string{"rm"},
		Short:   "删除指定任务并清理 Broker 保留状态与本地关联文件",
		Long: `删除指定任务。此操作会：
1. 从 MQTT Broker 中清除该任务的保留状态 (agentpulse/v1/machines/{machine_id}/jobs/{job_id}/status)；
2. 清理内存 Store 及本地 .agentpulse/ 目录中该任务关联的产物与快照记录；
3. 如果任务处于运行中状态，将拒绝删除。`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]

			st, err := getStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()

			job, err := st.GetJob(ctx, jobID)
			if err != nil {
				return fmt.Errorf("任务不存在或未找到: %s", jobID)
			}

			if job.Status == model.StatusRunning {
				return fmt.Errorf("任务 %s 正在运行中，无法删除", jobID)
			}

			if !force && termutil.IsTerminal(os.Stdin) {
				fmt.Fprintf(cmd.OutOrStdout(), "确认删除任务 %s (%s, %s)? [y/N]: ", job.ID, job.MachineID, job.Status)
				var response string
				fmt.Fscanln(cmd.InOrStdin(), &response)
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "操作已取消。")
					return nil
				}
			}

			if err := st.DeleteJob(ctx, jobID); err != nil {
				return fmt.Errorf("删除任务失败: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "已成功删除任务 %s 并清理保留状态。\n", jobID)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "强制删除，跳过确认提示")
	cmd.Flags().BoolVarP(&force, "yes", "y", false, "跳过确认提示")
	return cmd
}
