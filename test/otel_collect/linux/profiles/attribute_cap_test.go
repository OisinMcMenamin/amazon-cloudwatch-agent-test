// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/amazon-cloudwatch-agent-test/test/status"
)

func TestBuildAttributeCapPayload(t *testing.T) {
	payload := buildAttributeCapPayload("svc", "i-0", capExtraAttributes)
	var req struct {
		ResourceProfiles []struct {
			Resource struct {
				Attributes []resourceAttr `json:"attributes"`
			} `json:"resource"`
			ScopeProfiles []struct {
				Profiles []struct {
					AttributeIndices []int `json:"attributeIndices"`
				} `json:"profiles"`
			} `json:"scopeProfiles"`
		} `json:"resourceProfiles"`
		Dictionary struct {
			StringTable    []string `json:"stringTable"`
			AttributeTable []struct {
				KeyStrindex int `json:"keyStrindex"`
				Value       struct {
					StringValue string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributeTable"`
		} `json:"dictionary"`
	}
	require.NoError(t, json.Unmarshal(payload, &req))
	require.Len(t, req.ResourceProfiles, 2, "the request has to mix a valid and an oversized resource")

	valid := req.ResourceProfiles[0]
	require.Len(t, valid.Resource.Attributes, 2)
	require.Empty(t, valid.ScopeProfiles[0].Profiles[0].AttributeIndices)

	oversized := req.ResourceProfiles[1]
	require.Len(t, oversized.Resource.Attributes, 2, "over-cap attributes must not sit on the resource")
	require.Equal(t, serviceNameKey, oversized.Resource.Attributes[0].Key)
	indices := oversized.ScopeProfiles[0].Profiles[0].AttributeIndices
	require.Len(t, indices, capExtraAttributes)
	require.Greater(t, len(indices)+len(oversized.Resource.Attributes), 150)

	require.Len(t, req.Dictionary.AttributeTable, capExtraAttributes+1)
	require.Equal(t, 0, req.Dictionary.AttributeTable[0].KeyStrindex, "attribute table slot 0 is the empty sentinel")
	for i, idx := range indices {
		require.Greater(t, idx, 0)
		entry := req.Dictionary.AttributeTable[idx]
		require.Equal(t, "x", entry.Value.StringValue)
		require.Equal(t, fmt.Sprintf("integ.test.attr.%03d", i), req.Dictionary.StringTable[entry.KeyStrindex])
	}
}

func TestParseAgentProcess(t *testing.T) {
	p := parseAgentProcess("MainPID=1234\nNRestarts=0\nActiveState=active\n")
	require.Equal(t, agentProcess{mainPID: "1234", restarts: "0", activeState: "active"}, p)
}

func TestCheckDeliveredTruncated(t *testing.T) {
	trimLine := `W! {"caller":"awsattributelimitprocessor/processor.go:90","msg":"dropped attributes to meet limit","otelcol.component.id":"awsattributelimit/profiles","dropped":12}`
	healthy := &ProfilesAttributeCapTestRunner{done: true}
	healthy.baseline = exporterCounts{found: true, sent: 10}
	healthy.final = exporterCounts{found: true, sent: 25}

	ok := healthy.checkDeliveredTruncated([]string{trimLine}, nil)
	require.Equal(t, status.SUCCESSFUL, ok.Status, "%v", ok.Reason)

	partial := healthy.checkDeliveredTruncated([]string{
		trimLine,
		`W! {"caller":"otlphttpexporter/otlp.go:455","msg":"Partial success response","otelcol.component.id":"otlp_http/profiles","dropped_samples":1}`,
	}, nil)
	require.Equal(t, status.FAILED, partial.Status)
	require.ErrorContains(t, partial.Reason, "backend rejected part of the request")

	failed := healthy.checkDeliveredTruncated([]string{trimLine, `E! Exporting failed. Dropping data. otlp_http/profiles 404`}, nil)
	require.ErrorContains(t, failed.Reason, "export failed after the oversized push")

	untrimmed := healthy.checkDeliveredTruncated(nil, nil)
	require.ErrorContains(t, untrimmed.Reason, "did not trim the oversized profile")

	stalled := &ProfilesAttributeCapTestRunner{done: true}
	stalled.baseline = exporterCounts{found: true, sent: 10}
	stalled.final = exporterCounts{found: true, sent: 10}
	require.ErrorContains(t, stalled.checkDeliveredTruncated([]string{trimLine}, nil).Reason, "sent counter did not rise")

	sendFailed := &ProfilesAttributeCapTestRunner{done: true}
	sendFailed.baseline = exporterCounts{found: true, sent: 10}
	sendFailed.final = exporterCounts{found: true, sent: 20, sendFailed: 1}
	require.ErrorContains(t, sendFailed.checkDeliveredTruncated([]string{trimLine}, nil).Reason, "failed to send")

	unreadable := healthy.checkDeliveredTruncated(nil, errors.New("no log"))
	require.ErrorContains(t, unreadable.Reason, "no log")
}
