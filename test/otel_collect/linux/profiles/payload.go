// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package profiles

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type resourceAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func attr(key, value string) resourceAttr {
	a := resourceAttr{Key: key}
	a.Value.StringValue = value
	return a
}

type resourceProfile struct {
	resource     []resourceAttr
	profileAttrs []resourceAttr
}

func profileIDJSON(seed uint64) string {
	return hex.EncodeToString(profileIDBytes(seed))
}

func buildProfilesPayload(serviceName, instanceID string) []byte {
	var attrs []resourceAttr
	if serviceName != "" {
		attrs = append(attrs, attr(serviceNameKey, serviceName))
	}
	attrs = append(attrs, attr("instance_id", instanceID))
	return buildResourceProfilesPayload(attrs)
}

func buildResourceProfilesPayload(resources ...[]resourceAttr) []byte {
	rps := make([]resourceProfile, 0, len(resources))
	for _, attrs := range resources {
		rps = append(rps, resourceProfile{resource: attrs})
	}
	return buildResourceProfilesPayloadWithAttrs(rps...)
}

func buildResourceProfilesPayloadWithAttrs(resources ...resourceProfile) []byte {
	seed := unixNano(time.Now())
	stringTable := append([]string(nil), payloadStringTable...)
	var attributeTable []any
	resourceProfiles := make([]any, 0, len(resources))
	for _, rp := range resources {
		seed++
		ts := fmt.Sprint(seed)
		profile := map[string]any{
			"sampleType": map[string]any{"typeStrindex": 1, "unitStrindex": 2},
			"samples": []any{map[string]any{
				"stackIndex":         1,
				"values":             []string{"100"},
				"timestampsUnixNano": []string{ts},
			}},
			"timeUnixNano": ts,
			"durationNano": "10000000000",
			"periodType":   map[string]any{"typeStrindex": 3, "unitStrindex": 4},
			"period":       "10000000",
			"profileId":    profileIDJSON(seed),
		}
		if len(rp.profileAttrs) > 0 {
			if len(attributeTable) == 0 {
				attributeTable = append(attributeTable, map[string]any{})
			}
			indices := make([]int, 0, len(rp.profileAttrs))
			for _, a := range rp.profileAttrs {
				stringTable = append(stringTable, a.Key)
				attributeTable = append(attributeTable, map[string]any{
					"keyStrindex": len(stringTable) - 1,
					"value":       map[string]any{"stringValue": a.Value.StringValue},
				})
				indices = append(indices, len(attributeTable)-1)
			}
			profile["attributeIndices"] = indices
		}
		resourceProfiles = append(resourceProfiles, map[string]any{
			"resource": map[string]any{"attributes": rp.resource},
			"scopeProfiles": []any{map[string]any{
				"scope":    map[string]any{"name": "cloudwatch-agent-integ-test", "version": "1.0.0"},
				"profiles": []any{profile},
			}},
		})
	}
	dictionary := map[string]any{
		"mappingTable": []any{map[string]any{}},
		"locationTable": []any{
			map[string]any{},
			map[string]any{"address": "4096", "lines": []any{map[string]any{"functionIndex": 1, "line": "10"}}},
			map[string]any{"address": "8192", "lines": []any{map[string]any{"functionIndex": 2, "line": "20"}}},
		},
		"functionTable": []any{
			map[string]any{},
			map[string]any{"nameStrindex": 5, "filenameStrindex": 7},
			map[string]any{"nameStrindex": 6, "filenameStrindex": 7},
		},
		"stringTable": stringTable,
		"stackTable":  []any{map[string]any{}, map[string]any{"locationIndices": []int{1, 2}}},
		"linkTable":   []any{map[string]any{}},
	}
	if len(attributeTable) > 0 {
		dictionary["attributeTable"] = attributeTable
	}
	payload := map[string]any{
		"resourceProfiles": resourceProfiles,
		"dictionary":       dictionary,
	}
	out, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return out
}
