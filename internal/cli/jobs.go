package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/spf13/cobra"
)

func newJobsCmd() *cobra.Command {
	var jsonOutput bool
	var groupBy string

	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "查看全网任务列表及执行状态",
		Long: `从消息存储中检索并汇总跨机器的全部 AI Agent 任务（包含运行中、排队中、已完成与失败任务）。
支持短名称、项目/仓库名、状态、开始时间等字段展示，支持按多种维度分组查看，支持 JSON 输出。`,
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

			normGroup := strings.ToLower(strings.TrimSpace(groupBy))
			if normGroup != "" && normGroup != "none" {
				renderGroupedJobsTable(out, jobs, normGroup)
				return nil
			}

			renderJobsTable(out, jobs)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "以 JSON 格式输出任务列表")
	cmd.Flags().StringVar(&groupBy, "group-by", "", "按指定维度分组展示 (可选: repo, machine, status, engine, started_at, requested_by)")
	cmd.AddCommand(newJobsDeleteCmd())
	return cmd
}

func renderJobsTable(out io.Writer, jobs []model.Job) {
	fmt.Fprintf(out, "%s %s %s %s %s %s %s\n",
		termutil.PadRight("短名/标题", 22),
		termutil.PadRight("项目/仓库", 16),
		termutil.PadRight("状态", 10),
		termutil.PadRight("开始时间", 16),
		termutil.PadRight("任务 ID", 16),
		termutil.PadRight("节点", 12),
		termutil.PadRight("运行时长", 8))
	fmt.Fprintln(out, strings.Repeat("-", 106))

	for _, j := range jobs {
		title := termutil.TruncateString(j.DisplayTitle(), 20, "...")
		repo := j.RepoOrProject()
		if repo == "" {
			repo = "-"
		} else {
			repo = termutil.TruncateString(repo, 15, "...")
		}
		started := "-"
		if !j.StartedAt.IsZero() {
			started = j.StartedAt.Local().Format("01-02 15:04:05")
		}
		dur := j.DurationStr
		if dur == "" {
			dur = "-"
		}
		status := strings.ToUpper(string(j.Status))
		idStr := termutil.TruncateString(j.ID, 16, "...")
		machStr := termutil.TruncateString(j.MachineID, 12, "...")

		fmt.Fprintf(out, "%s %s %s %s %s %s %s\n",
			termutil.PadRight(title, 22),
			termutil.PadRight(repo, 16),
			termutil.PadRight(status, 10),
			termutil.PadRight(started, 16),
			termutil.PadRight(idStr, 16),
			termutil.PadRight(machStr, 12),
			termutil.PadRight(dur, 8))
	}
}

func renderGroupedJobsTable(out io.Writer, jobs []model.Job, groupField string) {
	type groupEntry struct {
		name string
		jobs []model.Job
	}

	getGroupKey := func(j model.Job) string {
		switch groupField {
		case "repo", "project":
			r := j.RepoOrProject()
			if r == "" {
				return "未知/未标注"
			}
			return r
		case "machine", "node":
			m := strings.TrimSpace(j.MachineID)
			if m == "" {
				return "未知/未标注"
			}
			return m
		case "status":
			s := strings.TrimSpace(string(j.Status))
			if s == "" {
				return "未知/未标注"
			}
			return strings.ToUpper(s)
		case "engine":
			e := strings.TrimSpace(string(j.Engine))
			if e == "" {
				return "未知/未标注"
			}
			return e
		case "started_at", "started":
			if j.StartedAt.IsZero() {
				return "未知/未标注"
			}
			return j.StartedAt.Local().Format("2006-01-02")
		case "requested_by", "requested":
			r := strings.TrimSpace(j.RequestedBy)
			if r == "" {
				return "未知/未标注"
			}
			return r
		default:
			return "未知/未标注"
		}
	}

	groupsMap := make(map[string][]model.Job)
	var groupKeys []string
	for _, j := range jobs {
		k := getGroupKey(j)
		if _, ok := groupsMap[k]; !ok {
			groupKeys = append(groupKeys, k)
		}
		groupsMap[k] = append(groupsMap[k], j)
	}

	for _, k := range groupKeys {
		gJobs := groupsMap[k]
		fmt.Fprintf(out, "\n▶ 分组 [%s: %s] (%d jobs)\n", groupField, k, len(gJobs))
		renderJobsTable(out, gJobs)
	}
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
