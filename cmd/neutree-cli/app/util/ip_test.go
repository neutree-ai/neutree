package util

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetHostIPPrefersKubernetesHostInterface(t *testing.T) {
	ip, err := getHostIP(
		func() (net.IP, error) {
			return net.ParseIP("10.0.0.42"), nil
		},
		func() ([]net.Addr, error) {
			return []net.Addr{
				mustParseCIDR(t, "172.17.0.1/16"),
				mustParseCIDR(t, "192.168.1.12/24"),
			}, nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "10.0.0.42", ip)
}

func TestGetHostIPFallsBackToInterfaceAddress(t *testing.T) {
	ip, err := getHostIP(
		func() (net.IP, error) {
			return nil, errors.New("no default route")
		},
		func() ([]net.Addr, error) {
			return []net.Addr{
				mustParseCIDR(t, "127.0.0.1/8"),
				mustParseCIDR(t, "192.168.1.12/24"),
			}, nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "192.168.1.12", ip)
}

func TestGetHostIPRejectsIPv6HostInterface(t *testing.T) {
	_, err := getHostIP(
		func() (net.IP, error) {
			return net.ParseIP("2001:db8::1"), nil
		},
		func() ([]net.Addr, error) {
			t.Fatal("should not fall back when Kubernetes host interface returns an IPv6 address")
			return nil, nil
		},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no valid host IPv4 found")
}

func mustParseCIDR(t *testing.T, cidr string) net.Addr {
	t.Helper()

	ip, ipNet, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	ipNet.IP = ip

	return ipNet
}
