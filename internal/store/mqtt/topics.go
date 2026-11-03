package mqtt

import (
	"fmt"
	"strings"
)

const (
	// TopicRoot is the common namespace prefix for all AgentPulse v1 topics.
	TopicRoot = "agentpulse/v1"

	// TopicMachinesStatus subscribes to all machine heartbeats and online statuses.
	TopicMachinesStatus = "agentpulse/v1/machines/+/status"

	// TopicJobsStatus subscribes to all job lifecycle and progress updates.
	TopicJobsStatus = "agentpulse/v1/machines/+/jobs/+/status"

	// TopicJobsLogs subscribes to real-time stdout/stderr log streams of jobs.
	TopicJobsLogs = "agentpulse/v1/machines/+/jobs/+/logs"

	// TopicMachinesDispatch subscribes to all job dispatch commands across machines.
	TopicMachinesDispatch = "agentpulse/v1/machines/+/commands/dispatch"
)

// MachineDispatchTopic returns the dispatch command topic for a specific machine.
func MachineDispatchTopic(machineID string) string {
	return fmt.Sprintf("agentpulse/v1/machines/%s/commands/dispatch", machineID)
}

// MachineStatusTopic returns the status topic for a specific machine.
func MachineStatusTopic(machineID string) string {
	return fmt.Sprintf("agentpulse/v1/machines/%s/status", machineID)
}

// JobStatusTopic returns the status topic for a specific job on a machine.
func JobStatusTopic(machineID, jobID string) string {
	return fmt.Sprintf("agentpulse/v1/machines/%s/jobs/%s/status", machineID, jobID)
}

// JobLogsTopic returns the logs topic for a specific job on a machine.
func JobLogsTopic(machineID, jobID string) string {
	return fmt.Sprintf("agentpulse/v1/machines/%s/jobs/%s/logs", machineID, jobID)
}

type topicType int

const (
	topicUnknown topicType = iota
	topicMachineStatus
	topicJobStatus
	topicJobLog
	topicCommandDispatch
)

type parsedTopic struct {
	typ       topicType
	machineID string
	jobID     string
}

// parseTopic breaks down an incoming MQTT topic into its components.
func parseTopic(topic string) parsedTopic {
	clean := strings.Trim(topic, "/")
	parts := strings.Split(clean, "/")

	// Minimum topic length is 5: agentpulse/v1/machines/{machine_id}/status
	if len(parts) < 5 || parts[0] != "agentpulse" || parts[1] != "v1" || parts[2] != "machines" {
		return parsedTopic{typ: topicUnknown}
	}

	machineID := parts[3]
	if machineID == "" {
		return parsedTopic{typ: topicUnknown}
	}

	if len(parts) == 5 && parts[4] == "status" {
		return parsedTopic{
			typ:       topicMachineStatus,
			machineID: machineID,
		}
	}

	if len(parts) == 6 && parts[4] == "commands" && parts[5] == "dispatch" {
		return parsedTopic{
			typ:       topicCommandDispatch,
			machineID: machineID,
		}
	}

	if len(parts) == 7 && parts[4] == "jobs" {
		jobID := parts[5]
		if jobID == "" {
			return parsedTopic{typ: topicUnknown}
		}
		switch parts[6] {
		case "status":
			return parsedTopic{
				typ:       topicJobStatus,
				machineID: machineID,
				jobID:     jobID,
			}
		case "logs":
			return parsedTopic{
				typ:       topicJobLog,
				machineID: machineID,
				jobID:     jobID,
			}
		}
	}

	return parsedTopic{typ: topicUnknown}
}

