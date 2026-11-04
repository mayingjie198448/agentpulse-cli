package mqtt

import (
	"testing"
)

func TestParseTopic(t *testing.T) {
	tests := []struct {
		topic     string
		wantType  topicType
		machineID string
		jobID     string
	}{
		{
			topic:     "agentpulse/v1/machines/mac-studio-m2/status",
			wantType:  topicMachineStatus,
			machineID: "mac-studio-m2",
		},
		{
			topic:     "/agentpulse/v1/machines/ccdq/status/",
			wantType:  topicMachineStatus,
			machineID: "ccdq",
		},
		{
			topic:     "agentpulse/v1/machines/mac-studio-m2/jobs/job-001/status",
			wantType:  topicJobStatus,
			machineID: "mac-studio-m2",
			jobID:     "job-001",
		},
		{
			topic:     "agentpulse/v1/machines/linux-box/jobs/job-agy-05/logs",
			wantType:  topicJobLog,
			machineID: "linux-box",
			jobID:     "job-agy-05",
		},
		{
			topic:     "agentpulse/v1/mac-studio-m2/announce",
			wantType:  topicAnnounce,
			machineID: "mac-studio-m2",
		},
		{
			topic:     "agentpulse/v1/machines/mac-studio-m2/announce",
			wantType:  topicAnnounce,
			machineID: "mac-studio-m2",
		},
		{
			topic:     "agentpulse/v1/machines/mac-studio-m2/commands/dispatch",
			wantType:  topicCommandDispatch,
			machineID: "mac-studio-m2",
		},
		// Invalid topics
		{
			topic:    "agentpulse/v2/machines/ccdq/status",
			wantType: topicUnknown,
		},
		{
			topic:    "agentpulse/v1/machines//status",
			wantType: topicUnknown,
		},
		{
			topic:    "agentpulse/v1/machines/ccdq/jobs//status",
			wantType: topicUnknown,
		},
		{
			topic:    "other/topic",
			wantType: topicUnknown,
		},
		{
			topic:    "agentpulse/v1/machines/ccdq/unknown",
			wantType: topicUnknown,
		},
		{
			topic:    "agentpulse/v1/machines/ccdq/jobs/job-1/unknown",
			wantType: topicUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			parsed := parseTopic(tt.topic)
			if parsed.typ != tt.wantType {
				t.Errorf("got type %v, want %v", parsed.typ, tt.wantType)
			}
			if parsed.machineID != tt.machineID {
				t.Errorf("got machineID %q, want %q", parsed.machineID, tt.machineID)
			}
			if parsed.jobID != tt.jobID {
				t.Errorf("got jobID %q, want %q", parsed.jobID, tt.jobID)
			}
		})
	}
}

func TestTopicHelpers(t *testing.T) {
	if got := MachineDispatchTopic("node1"); got != "agentpulse/v1/machines/node1/commands/dispatch" {
		t.Errorf("MachineDispatchTopic: got %s", got)
	}
	if got := MachineStatusTopic("node1"); got != "agentpulse/v1/machines/node1/status" {
		t.Errorf("MachineStatusTopic: got %s", got)
	}
	if got := JobStatusTopic("node1", "job-1"); got != "agentpulse/v1/machines/node1/jobs/job-1/status" {
		t.Errorf("JobStatusTopic: got %s", got)
	}
	if got := JobLogsTopic("node1", "job-1"); got != "agentpulse/v1/machines/node1/jobs/job-1/logs" {
		t.Errorf("JobLogsTopic: got %s", got)
	}
}
