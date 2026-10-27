package utils

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// GetEndpointServeAddress returns a direct cluster target with escaped route segments.
func GetEndpointServeAddress(cluster *v1.Cluster, endpoint *v1.Endpoint) (*util.URLComponents, error) {
	scheme, host, port, err := util.GetClusterServeAddress(cluster)
	if err != nil {
		return nil, err
	}

	if scheme != util.SchemeHTTP && scheme != util.SchemeHTTPS {
		return nil, errors.New("cluster serve address must use HTTP or HTTPS")
	}

	return &util.URLComponents{
		Scheme: scheme,
		Host:   host,
		Port:   port,
		Path:   "/" + url.PathEscape(endpoint.Metadata.Workspace) + "/" + url.PathEscape(endpoint.Metadata.Name),
	}, nil
}

// FormatServiceURL returns an endpoint's direct cluster serving route.
func FormatServiceURL(cluster *v1.Cluster, endpoint *v1.Endpoint) (string, error) {
	address, err := GetEndpointServeAddress(cluster, endpoint)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s://%s%s", address.Scheme, net.JoinHostPort(address.Host, strconv.Itoa(address.Port)), address.Path), nil
}
