// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package profiles

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	profilesPipelineID = "profiles/otlp"

	profilesResourceProcessorID = "resource/profiles"

	profilesIdentityProcessorID = "transform/identity"

	serviceNameKey = "service.name"

	fallbackServiceName = "unknown_service"
)

type collectorConfig struct {
	Processors map[string]processorConfig `yaml:"processors"`
	Service    struct {
		Pipelines map[string]pipelineConfig `yaml:"pipelines"`
	} `yaml:"service"`
}

type pipelineConfig struct {
	Receivers  []string `yaml:"receivers"`
	Processors []string `yaml:"processors"`
	Exporters  []string `yaml:"exporters"`
}

type processorConfig struct {
	Attributes []attributeAction `yaml:"attributes"`
	// profile_statements on the transform processor. The agent renders it as a list of OTTL
	// statement groups (one per context), not a flat list of statement strings.
	ProfileStatements []statementGroup `yaml:"profile_statements"`
}

type statementGroup struct {
	Context    string   `yaml:"context"`
	Statements []string `yaml:"statements"`
}

type attributeAction struct {
	Action string `yaml:"action"`
	Key    string `yaml:"key"`
	Value  any    `yaml:"value"`
}

type profilesPipeline struct {
	pipelineConfig
	resourceActions []attributeAction
	// identityProfileStatements are the OTTL profile statements on transform/identity, flattened
	// across its statement groups. The nil-guarded unknown_service fallback lives here.
	identityProfileStatements []string
}

func parseProfilesPipeline(content []byte) (profilesPipeline, error) {
	var cfg collectorConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return profilesPipeline{}, fmt.Errorf("parsing translated config: %w", err)
	}
	pipeline, ok := cfg.Service.Pipelines[profilesPipelineID]
	if !ok {
		return profilesPipeline{}, fmt.Errorf("translated config has no %s pipeline", profilesPipelineID)
	}
	return profilesPipeline{
		pipelineConfig:            pipeline,
		resourceActions:           cfg.Processors[profilesResourceProcessorID].Attributes,
		identityProfileStatements: flattenStatements(cfg.Processors[profilesIdentityProcessorID].ProfileStatements),
	}, nil
}

func flattenStatements(groups []statementGroup) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g.Statements...)
	}
	return out
}

func componentType(id string) string {
	typ, _, _ := strings.Cut(id, "/")
	return typ
}

// checkProfilesPipelineInvariants enforces two properties of the generated profiles pipeline:
//   - no batch processor: the batch processor has no profiles support, so placing it in this
//     pipeline makes collector startup fail and takes every other pipeline down with it.
//   - service.name is always set: the backend rejects a resource without one. The agent sets it
//     with one of two shapes depending on platform, and either satisfies the invariant:
//     resource/profiles inserts an inferred name (EC2 with a host tag or instance role, or ECS
//     from the task family); or, where there is nothing to infer (EC2 with no tag/role, and
//     Kubernetes, which has no resource/profiles at all), transform/identity carries a
//     nil-guarded unknown_service fallback so clients that set no name still get one.
func checkProfilesPipelineInvariants(p profilesPipeline) error {
	var problems []string
	hasResource := false
	for _, id := range p.Processors {
		if componentType(id) == "batch" {
			problems = append(problems, fmt.Sprintf("processor %s is in the %s pipeline", id, profilesPipelineID))
		}
		if id == profilesResourceProcessorID {
			hasResource = true
		}
	}
	resourceInserts := hasResource && len(serviceNameInserts(p.resourceActions)) > 0
	identityFallback := hasFallbackServiceName(p.identityProfileStatements)
	if !resourceInserts && !identityFallback {
		problems = append(problems, fmt.Sprintf(
			"%s pipeline sets no %s: expected an insert or upsert on %s or a nil-guarded %q statement on %s (processors: %v)",
			profilesPipelineID, serviceNameKey, profilesResourceProcessorID, fallbackServiceName, profilesIdentityProcessorID, p.Processors))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// hasFallbackServiceName reports whether any statement sets service.name to the unknown_service
// fallback under a nil guard, i.e. set(resource.attributes["service.name"], "unknown_service")
// where resource.attributes["service.name"] == nil. This is the mechanism that owns the final
// service.name on platforms with no inferred insert, so it must not fire when a name is present.
func hasFallbackServiceName(statements []string) bool {
	quotedFallback := fmt.Sprintf("%q", fallbackServiceName)
	for _, s := range statements {
		if strings.Contains(s, "set(") &&
			strings.Contains(s, serviceNameKey) &&
			strings.Contains(s, quotedFallback) &&
			strings.Contains(s, "nil") {
			return true
		}
	}
	return false
}

// In order; with insert actions the first value wins, because a later insert skips a key that is
// already present.
func serviceNameInserts(actions []attributeAction) []string {
	var values []string
	for _, a := range actions {
		if a.Key != serviceNameKey {
			continue
		}
		if a.Action == "insert" || a.Action == "upsert" {
			values = append(values, fmt.Sprint(a.Value))
		}
	}
	return values
}
