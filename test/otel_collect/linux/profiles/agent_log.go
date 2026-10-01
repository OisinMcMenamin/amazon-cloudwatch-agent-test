// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package profiles

import (
	"fmt"
	"strings"

	"github.com/aws/amazon-cloudwatch-agent-test/util/common"
)

// These are collector-internal log strings the profiles checks match. They are the exporter
// helper's and OTLP exporter's own messages, verified against collector v0.150.0; update them if
// the pinned collector version changes the wording.
const (
	// exportFailedLogText is the exporter helper's message for a failed export. Retryable failures
	// (throttling, 5xx, network) are logged with it at info level and only escalate to an error
	// once retries run out, so the text is matched at any level.
	exportFailedLogText = "exporting failed"
	// exportDroppingLogText and exportRejectingLogText are logged at error level when the exporter
	// gives up on an export (drops it) or refuses it (rejects it).
	exportDroppingLogText  = "Dropping data"
	exportRejectingLogText = "Rejecting"
	// partialSuccessLogText is the exporter's 2xx partial success response: the backend accepted
	// the request but dropped some records.
	partialSuccessLogText = "Partial success response"
	// componentIDLogKey is the log field carrying the exporter/component id, e.g.
	// "otelcol.component.id":"otlp_http/profiles".
	componentIDLogKey = "otelcol.component.id"
)

// profilesLogLines returns the agent log lines that mention profiles. Each runner starts the
// agent with a fresh log (see resetAgentLog), so these are the current runner's lines only.
func profilesLogLines() ([]string, error) {
	content, err := common.RunCommand("sudo cat " + common.AgentLogFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent log %s: %w", common.AgentLogFile, err)
	}
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "profiles") {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// lastExportFailure returns the last failed-export line, or "" when there is none.
func lastExportFailure(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(lines[i]), exportFailedLogText) {
			return lines[i]
		}
	}
	return ""
}

// profilesExporterLogProblem reports whether a profiles agent-log line is an export problem that
// must fail Profiles_ExporterHealth. A retried-then-delivered export logs "Exporting failed. Will
// retry the request after interval." at info level and is not a problem on its own; delivery is
// covered by Profiles_ExporterDelivery from the exporter counters. Error-level drop and reject
// lines, any other E! line, and a partial success response are problems.
func profilesExporterLogProblem(line string) bool {
	if !strings.Contains(line, "profiles") {
		return false
	}
	if strings.Contains(line, "E! ") {
		return true
	}
	if strings.Contains(line, exportDroppingLogText) || strings.Contains(line, exportRejectingLogText) {
		return true
	}
	return strings.Contains(line, partialSuccessLogText)
}

// resetAgentLog removes the agent log before a runner starts the agent, so the log checks of
// one runner never see lines another runner's agent run wrote. The agent creates a new log when
// it starts. Without this, running the whole package left the attribute cap test's expected
// partial success warning in the log, and the main test's exporter health check failed on it.
func resetAgentLog() {
	common.RecreateAgentLogfile(common.AgentLogFile)
}
