package semver

import "github.com/Masterminds/semver/v3"

func LessThan(a, b string) (bool, error) {
	versionA, err := semver.NewVersion(a)
	if err != nil {
		return false, err
	}

	versionB, err := semver.NewVersion(b)
	if err != nil {
		return false, err
	}

	return versionA.LessThan(versionB), nil
}
