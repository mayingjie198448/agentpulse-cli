package mqtt

import "strings"

const (
	// TopicRoot is the common namespace prefix for all AgentPulse v1 topics.
	TopicRoot = "agentpulse/v1"

	// TopicMachinesStatus subscribes to all machine heartbeats and online statuses.
	TopicMachinesStatus = "agentpulse/v1/machines/+/status"

	// TopicJobsStatus subscribes to all job lifecycle and progress updates.
	TopicJobsStatus = "agentpulse/v1/machines/+/jobs/+/status"

	// TopicJobsLogs subscribes to real-time stdout/stderr log streams of jobs.
	TopicJobsLogs = "agentpulse/v1/machines/+/jobs/+/logs"
)

type topicType int

const (
	topicUnknown topicType = iota
	topicMachineStatus
	topicJobStatus
	topicJobLog
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
