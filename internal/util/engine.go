package util

import v1 "github.com/neutree-ai/neutree/api/v1"

func MergeEngine(existing, new *v1.Engine) *v1.Engine {
	for _, newVersion := range new.Spec.Versions {
		found := false

		for idx, oldVersion := range existing.Spec.Versions {
			if oldVersion.Version == newVersion.Version {
				// merge
				existing.Spec.Versions[idx] = MergeEngineVersion(oldVersion, newVersion)
				found = true

				break
			}
		}

		if !found {
			existing.Spec.Versions = append(existing.Spec.Versions, newVersion)
		}
	}

	for _, newTask := range new.Spec.SupportedTasks {
		found := false

		for _, oldTask := range existing.Spec.SupportedTasks {
			if oldTask == newTask {
				found = true
				break
			}
		}

		if !found {
			existing.Spec.SupportedTasks = append(existing.Spec.SupportedTasks, newTask)
		}
	}

	return existing
}

func MergeEngineVersion(existing, new *v1.EngineVersion) *v1.EngineVersion {
	// merge oldVersion with newVersion
	if existing.Images == nil {
		existing.Images = make(map[string]*v1.EngineImage)
	}

	for key := range new.Images {
		existing.Images[key] = new.Images[key]
	}

	if existing.DeployTemplate == nil {
		existing.DeployTemplate = make(map[string]map[string]string)
	}

	for clusterType := range new.DeployTemplate {
		if existing.DeployTemplate[clusterType] == nil {
			existing.DeployTemplate[clusterType] = make(map[string]string)
		}

		for deployMode := range new.DeployTemplate[clusterType] {
			existing.DeployTemplate[clusterType][deployMode] = new.DeployTemplate[clusterType][deployMode]
		}
	}

	if new.ValuesSchema != nil {
		existing.ValuesSchema = new.ValuesSchema
	}

	mergeEngineCapabilities(existing, new)

	for idx := range new.SupportedTasks {
		found := false

		for _, oldTask := range existing.SupportedTasks {
			if oldTask == new.SupportedTasks[idx] {
				found = true
				break
			}
		}

		if !found {
			existing.SupportedTasks = append(existing.SupportedTasks, new.SupportedTasks[idx])
		}
	}

	return existing
}

// mergeEngineCapabilities merges a capability declaration per capability, so a
// re-registration that declares only one capability leaves the others alone.
//
// Deliberately not the union semantics used for SupportedTasks above: a union
// can only ever add, which would make a capability impossible to switch off once
// declared. Here a declared capability replaces the previous one wholesale, so
// re-registering with {"enabled": false} genuinely disables it. An omitted
// (nil) capability still means "no opinion" and preserves what was there.
func mergeEngineCapabilities(existing, new *v1.EngineVersion) {
	if new.Capabilities == nil {
		return
	}

	if existing.Capabilities == nil {
		existing.Capabilities = &v1.EngineCapabilities{}
	}

	if new.Capabilities.MetricsExport != nil {
		existing.Capabilities.MetricsExport = new.Capabilities.MetricsExport
	}

	if new.Capabilities.Playground != nil {
		existing.Capabilities.Playground = new.Capabilities.Playground
	}
}
