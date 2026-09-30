// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package profiles

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aws/amazon-cloudwatch-agent-test/environment"
	"github.com/aws/amazon-cloudwatch-agent-test/test/status"
	"github.com/aws/amazon-cloudwatch-agent-test/test/test_runner"
	"github.com/aws/amazon-cloudwatch-agent-test/util/common"
)

func init() {
	environment.RegisterEnvironmentMetaDataFlags()
}

const (
	profilesRuntime    = 3 * time.Minute
	otlpHTTPEndpoint   = "http://127.0.0.1:4318"
	otlpHTTPAddr       = "127.0.0.1:4318"
	profilesURLPath    = "/v1development/profiles"
	profilesService    = "cwagent-integ-test-profiles"
	translatedYamlPath = "/opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.yaml"
	profilesExporterID = "otlp_http/profiles"
)

type ProfilesTestRunner struct {
	test_runner.BaseTestRunner
	env *environment.MetaData

	mu           sync.Mutex
	pushAttempts int
	pushAccepted int
	pushFailures []string
}

var _ test_runner.ITestRunner = (*ProfilesTestRunner)(nil)

func (t *ProfilesTestRunner) GetTestName() string                { return "OtelCollectProfiles" }
func (t *ProfilesTestRunner) GetAgentRunDuration() time.Duration { return profilesRuntime }
func (t *ProfilesTestRunner) GetAgentConfigFileName() string     { return "profiles_config.json" }
func (t *ProfilesTestRunner) GetMeasuredMetrics() []string       { return nil }

func (t *ProfilesTestRunner) SetupAfterAgentRun() error {
	go t.pushProfiles()
	return nil
}

func (t *ProfilesTestRunner) pushProfiles() {
	if err := common.WaitForTCPPort(otlpHTTPAddr, 2*time.Minute); err != nil {
		t.recordFailure(fmt.Sprintf("OTLP HTTP port never became ready: %v", err))
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	timeout := time.After(profilesRuntime - 30*time.Second)

	for {
		select {
		case <-timeout:
			return
		case <-ticker.C:
			t.pushOnce()
		}
	}
}

func (t *ProfilesTestRunner) pushOnce() {
	t.mu.Lock()
	t.pushAttempts++
	t.mu.Unlock()

	payload := buildProfilesPayload(profilesService, t.env.InstanceId)
	resp, err := http.Post(otlpHTTPEndpoint+profilesURLPath, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.recordFailure(fmt.Sprintf("POST %s failed: %v", profilesURLPath, err))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.recordFailure(fmt.Sprintf("POST %s returned %d: %s", profilesURLPath, resp.StatusCode, string(body)))
		return
	}

	t.mu.Lock()
	t.pushAccepted++
	t.mu.Unlock()
}

func (t *ProfilesTestRunner) recordFailure(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pushFailures = append(t.pushFailures, msg)
}

func (t *ProfilesTestRunner) Validate() status.TestGroupResult {
	return status.TestGroupResult{
		Name: t.GetTestName(),
		TestResults: []status.TestResult{
			t.validateTranslatedConfig(),
			t.validateReceiverAcceptance(),
			t.validateExporterHealth(),
		},
	}
}

func (t *ProfilesTestRunner) validateTranslatedConfig() status.TestResult {
	result := status.TestResult{Name: "Profiles_TranslatedConfig", Status: status.FAILED}

	yamlContent, err := common.RunCommand("sudo cat " + translatedYamlPath)
	if err != nil {
		result.Reason = fmt.Errorf("failed to read translated agent config %s: %w", translatedYamlPath, err)
		return result
	}

	for _, want := range []string{profilesExporterID, profilesURLPath} {
		if !strings.Contains(yamlContent, want) {
			result.Reason = fmt.Errorf("translated config %s does not contain %q — agent build does not generate a profiles pipeline from the opentelemetry otlp section", translatedYamlPath, want)
			return result
		}
	}

	result.Status = status.SUCCESSFUL
	return result
}

func (t *ProfilesTestRunner) validateReceiverAcceptance() status.TestResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := status.TestResult{Name: "Profiles_ReceiverAcceptance", Status: status.FAILED}
	log.Printf("[Profiles] push attempts: %d, accepted: %d, failures: %d", t.pushAttempts, t.pushAccepted, len(t.pushFailures))

	if len(t.pushFailures) > 0 {
		result.Reason = fmt.Errorf("profile pushes failed: %s", strings.Join(t.pushFailures, "; "))
		return result
	}
	if t.pushAccepted == 0 {
		result.Reason = fmt.Errorf("no profile payloads were accepted by the agent's OTLP receiver")
		return result
	}

	result.Status = status.SUCCESSFUL
	return result
}

func (t *ProfilesTestRunner) validateExporterHealth() status.TestResult {
	result := status.TestResult{Name: "Profiles_ExporterHealth", Status: status.FAILED}

	logContent, err := common.RunCommand("sudo cat " + common.AgentLogFile)
	if err != nil {
		result.Reason = fmt.Errorf("failed to read agent log %s: %w", common.AgentLogFile, err)
		return result
	}
	if len(logContent) == 0 {
		result.Reason = fmt.Errorf("agent log %s is empty", common.AgentLogFile)
		return result
	}

	var offending []string
	collectorReady := false
	for _, line := range strings.Split(logContent, "\n") {
		if strings.Contains(line, "Everything is ready. Begin running and processing data.") {
			collectorReady = true
		}
		if !strings.Contains(line, "profiles") {
			continue
		}
		if strings.Contains(line, "E! ") || strings.Contains(strings.ToLower(line), "partial success") {
			offending = append(offending, line)
		}
	}

	if !collectorReady {
		result.Reason = fmt.Errorf("agent log never reported the collector as ready")
		return result
	}
	if len(offending) > 0 {
		result.Reason = fmt.Errorf("agent log shows profiles export problems:\n%s", strings.Join(offending, "\n"))
		return result
	}

	result.Status = status.SUCCESSFUL
	return result
}

func buildProfilesPayload(serviceName, instanceID string) []byte {
	now := time.Now().UnixNano()
	profileID := fmt.Sprintf("%032x", now)
	payload := fmt.Sprintf(`{
  "resourceProfiles": [{
    "resource": {"attributes": [
      {"key": "service.name", "value": {"stringValue": "%s"}},
      {"key": "instance_id", "value": {"stringValue": "%s"}}
    ]},
    "scopeProfiles": [{
      "scope": {"name": "cloudwatch-agent-integ-test", "version": "1.0.0"},
      "profiles": [{
        "sampleType": {"typeStrindex": 1, "unitStrindex": 2},
        "samples": [{
          "stackIndex": 1,
          "values": ["100"],
          "timestampsUnixNano": ["%d"]
        }],
        "timeUnixNano": "%d",
        "durationNano": "10000000000",
        "periodType": {"typeStrindex": 3, "unitStrindex": 4},
        "period": "10000000",
        "profileId": "%s"
      }]
    }]
  }],
  "dictionary": {
    "mappingTable": [{}],
    "locationTable": [
      {},
      {"address": "4096", "lines": [{"functionIndex": 1, "line": "10"}]},
      {"address": "8192", "lines": [{"functionIndex": 2, "line": "20"}]}
    ],
    "functionTable": [
      {},
      {"nameStrindex": 5, "filenameStrindex": 7},
      {"nameStrindex": 6, "filenameStrindex": 7}
    ],
    "stringTable": ["", "samples", "count", "cpu", "nanoseconds", "testFunctionA", "testFunctionB", "test.go"],
    "stackTable": [{}, {"locationIndices": [1, 2]}],
    "linkTable": [{}]
  }
}`, serviceName, instanceID, now, now, profileID)
	return []byte(payload)
}

func TestProfiles(t *testing.T) {
	env := environment.GetEnvironmentMetaData()

	testRunner := &ProfilesTestRunner{
		BaseTestRunner: test_runner.BaseTestRunner{},
		env:            env,
	}
	runner := &test_runner.TestRunner{TestRunner: testRunner}
	result := runner.Run()

	for _, r := range result.TestResults {
		require.Equal(t, status.SUCCESSFUL, r.Status, "%s failed: %v", r.Name, r.Reason)
	}
}
