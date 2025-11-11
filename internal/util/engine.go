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
