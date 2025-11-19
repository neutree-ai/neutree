package helmclient

import (
    "context"
    "fmt"
    "os"

    

    "helm.sh/helm/v3/pkg/action"
    "helm.sh/helm/v3/pkg/chart"
    "helm.sh/helm/v3/pkg/chart/loader"
    "helm.sh/helm/v3/pkg/release"
    genericclioptions "k8s.io/cli-runtime/pkg/genericclioptions"
    "helm.sh/helm/v3/pkg/cli"
)

// HelmClient is an interface for interacting with Helm releases.
type HelmClient interface {
    UpgradeInstall(ctx context.Context, releaseName, chartPath, namespace string, values map[string]interface{}, setArgs []string) ([]byte, error)
}

// Helm client uses Helm SDK (helm.sh/helm/v3) to perform install/upgrade.

// SDKClient uses Helm SDK to install or upgrade a chart.
type SDKClient struct{}

func NewSDKClient() *SDKClient {
    return &SDKClient{}
}

func (s *SDKClient) UpgradeInstall(ctx context.Context, releaseName, chartPath, namespace string, values map[string]interface{}, setArgs []string) ([]byte, error) {
    // Use helm SDK to perform an upgrade/install
    settings := cli.New()
    actionConfig := new(action.Configuration)

    if err := actionConfigInit(actionConfig, settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), func(format string, v ...interface{}) {}); err != nil {
        return nil, err
    }

    // Try upgrade first; if fails with ErrReleaseNotFound, use install
    // build an upgrade client via wrapper so tests can override behavior
    uc := newUpgradeClient(actionConfig)
    uc.SetNamespace(namespace)
    uc.SetInstall(true)

    chart, err := loadChart(chartPath)
    if err != nil {
        return nil, err
    }

    // Convert values map to interface values expected by Run
    rel, err := uc.Run(releaseName, chart, values)
    if err != nil {
        return nil, err
    }

    return []byte(fmt.Sprintf("released: %s", rel.Name)), nil
}

// helper wrapper types and hooks to enable testing of SDK client without touching
// the real helm action implementation.
type upgradeClient interface {
    SetNamespace(ns string)
    SetInstall(b bool)
    Run(name string, ch *chart.Chart, values map[string]interface{}) (*release.Release, error)
}

type defaultUpgradeClient struct{ client *action.Upgrade }

func (d *defaultUpgradeClient) SetNamespace(ns string) { d.client.Namespace = ns }
func (d *defaultUpgradeClient) SetInstall(b bool)     { d.client.Install = b }
func (d *defaultUpgradeClient) Run(name string, ch *chart.Chart, values map[string]interface{}) (*release.Release, error) {
    return d.client.Run(name, ch, values)
}

var (
    // loadChart is a variable so tests can inject a failing loader
    loadChart = loader.Load
    // newUpgradeClient allows tests to inject a fake upgrade client
    newUpgradeClient = func(cfg *action.Configuration) upgradeClient { return &defaultUpgradeClient{client: action.NewUpgrade(cfg)} }
    // actionConfigInit is used to initialize the helm action config — tests may override this
    actionConfigInit = func(cfg *action.Configuration, getter genericclioptions.RESTClientGetter, namespace, driver string, log func(format string, v ...interface{})) error {
        return cfg.Init(getter, namespace, driver, log)
    }
)
