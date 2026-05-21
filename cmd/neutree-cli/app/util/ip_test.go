package util

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIPv4StringReturnsIPv4Address(t *testing.T) {
	ip, err := ipv4String(net.ParseIP("10.0.0.42"))

	require.NoError(t, err)
	assert.Equal(t, "10.0.0.42", ip)
}

func TestIPv4StringRejectsIPv6Address(t *testing.T) {
	_, err := ipv4String(net.ParseIP("2001:db8::1"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no valid host IPv4 found")
}

func TestGetFirstIPv4FromAddrsSkipsLoopbackAndIPv6(t *testing.T) {
	ip, err := getFirstIPv4FromAddrs([]net.Addr{
		mustParseCIDR(t, "127.0.0.1/8"),
		mustParseCIDR(t, "2001:db8::1/64"),
		mustParseCIDR(t, "192.168.1.12/24"),
	})

	require.NoError(t, err)
	assert.Equal(t, "192.168.1.12", ip)
}

func TestGetFirstIPv4FromAddrsReturnsErrorWhenNoIPv4Exists(t *testing.T) {
	_, err := getFirstIPv4FromAddrs([]net.Addr{
		mustParseCIDR(t, "127.0.0.1/8"),
		mustParseCIDR(t, "2001:db8::1/64"),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no valid host IP found")
}

func mustParseCIDR(t *testing.T, cidr string) net.Addr {
	t.Helper()

	ip, ipNet, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	ipNet.IP = ip

	return ipNet
}
