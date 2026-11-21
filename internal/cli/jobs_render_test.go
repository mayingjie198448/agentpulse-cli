package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

func TestRenderJobsTable_CJKAndEmoji(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	jobs := []model.Job{
		{
			ID:          "job-cjk-001",
			MachineID:   "mac-studio-m2",
			Engine:      model.EngineAgy,
			Status:      model.StatusRunning,
			Title:       "优化数据库连接池与查询性能处理长标题测试",
			Repo:        "mayingjie/微服务核心组件仓",
			StartedAt:   t0,
			DurationStr: "15m 30s",
		},
		{
			ID:          "job-emoji-002",
			MachineID:   "linux-gpu",
			Engine:      model.EngineOpenCode,
			Status:      model.StatusDone,
			Title:       "🚀🔥大模型微调评估✨🎉",
			Repo:        "org/🚀-models",
			StartedAt:   t0,
			DurationStr: "1h 5m",
		},
		{
			ID:          "job-ascii-003",
			MachineID:   "homelab-nas",
			Engine:      model.EngineAgy,
			Status:      model.StatusFailed,
			Title:       "Fix network timeout in sync module",
			Repo:        "owner/sync-daemon",
			StartedAt:   t0,
			DurationStr: "3m 12s",
		},
	}

	buf := new(bytes.Buffer)
	renderJobsTable(buf, jobs)

	output := buf.String()
	if !utf8.ValidString(output) {
		t.Fatalf("renderJobsTable output is not valid UTF-8: %s", output)
	}

	for _, header := range []string{"短名/标题", "项目/仓库", "状态", "开始时间", "任务 ID", "节点", "运行时长"} {
		if !strings.Contains(output, header) {
			t.Errorf("expected header %q in output:\n%s", header, output)
		}
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 5 { // 1 header + 1 separator + 3 job rows
		t.Errorf("expected 5 lines in table output, got %d:\n%s", len(lines), output)
	}
}

func TestRenderGroupedJobsTable_CJKAndDimensions(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	jobs := []model.Job{
		{
			ID:          "j1",
			MachineID:   "mac-1",
			Engine:      model.EngineAgy,
			Status:      model.StatusRunning,
			Title:       "任务一：连接池优化",
			Repo:        "org/repo-a",
			RequestedBy: "alice",
			StartedAt:   t0,
		},
		{
			ID:          "j2",
			MachineID:   "mac-2",
			Engine:      model.EngineOpenCode,
			Status:      model.StatusDone,
			Title:       "任务二：🚀模型量化",
			Repo:        "org/repo-b",
			RequestedBy: "bob",
			StartedAt:   t0,
		},
		{
			ID:        "j3",
			MachineID: "",
			Engine:    "",
			Status:    model.StatusQueued,
			Title:     "任务三：未标注",
		},
	}

	dimensions := []string{"repo", "machine", "status", "engine", "started_at", "requested_by"}
	for _, dim := range dimensions {
		t.Run("dim_"+dim, func(t *testing.T) {
			buf := new(bytes.Buffer)
			renderGroupedJobsTable(buf, jobs, dim)

			out := buf.String()
			if !utf8.ValidString(out) {
				t.Fatalf("renderGroupedJobsTable output is not valid UTF-8 for dim %s", dim)
			}
			if !strings.Contains(out, "▶ 分组") {
				t.Errorf("expected group banner for dim %s, got:\n%s", dim, out)
			}
		})
	}
}
