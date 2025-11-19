package helmclient

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	genericclioptions "k8s.io/cli-runtime/pkg/genericclioptions"
)

func TestUpgradeInstall_LoadChartError(t *testing.T) {
	oldLoad := loadChart
	oldInit := actionConfigInit
	defer func() { loadChart = oldLoad; actionConfigInit = oldInit }()

	// stub loader to return error
	loadChart = func(path string) (*chart.Chart, error) {
		return nil, fmt.Errorf("load failed")
	}

	// override init to a no-op so we don't need Kube config
	actionConfigInit = func(cfg *action.Configuration, getter genericclioptions.RESTClientGetter, namespace, driver string, log func(format string, v ...interface{})) error {
		return nil
	}

	c := NewSDKClient()
	_, err := c.UpgradeInstall(context.Background(), "rel", "/path/to/chart", "default", map[string]interface{}{}, nil)
	assert.Error(t, err)
}

type fakeUpgrade struct {
	namespace string
	release   *release.Release
	err       error
}

func (f *fakeUpgrade) SetNamespace(ns string) { f.namespace = ns }
func (f *fakeUpgrade) SetInstall(b bool)      {}
func (f *fakeUpgrade) Run(name string, ch *chart.Chart, values map[string]interface{}) (*release.Release, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.release, nil
}

func TestUpgradeInstall_RunErrorAndSuccess(t *testing.T) {
	oldLoad := loadChart
	oldNew := newUpgradeClient
	oldInit := actionConfigInit
	defer func() { loadChart = oldLoad; newUpgradeClient = oldNew; actionConfigInit = oldInit }()

	// stub chart loader to return a simple chart
	loadChart = func(path string) (*chart.Chart, error) {
		return &chart.Chart{Metadata: &chart.Metadata{Name: "fake"}}, nil
	}

	// stub init
	actionConfigInit = func(cfg *action.Configuration, getter genericclioptions.RESTClientGetter, namespace, driver string, log func(format string, v ...interface{})) error {
		return nil
	}

	// first test failing Run
	newUpgradeClient = func(cfg *action.Configuration) upgradeClient {
		return &fakeUpgrade{err: errors.New("upgrade failed")}
	}

	c := NewSDKClient()
	_, err := c.UpgradeInstall(context.Background(), "rel", "/path/to/chart", "default", map[string]interface{}{}, nil)
	assert.Error(t, err)

	// success path
	extra := &release.Release{Name: "myrel"}
	newUpgradeClient = func(cfg *action.Configuration) upgradeClient {
		return &fakeUpgrade{release: extra, err: nil}
	}

	out, err := c.UpgradeInstall(context.Background(), "rel", "/path/to/chart", "default", map[string]interface{}{}, nil)
	assert.NoError(t, err)
	assert.Contains(t, string(out), "released: myrel")
}
