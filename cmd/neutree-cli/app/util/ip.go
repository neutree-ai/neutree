package util

import (
	"fmt"
	"net"

	netutils "k8s.io/apimachinery/pkg/util/net"
)

// GetHostIP returns the IPv4 address used by launch templates.
// It first follows Kubernetes' host-interface selection so multi-NIC hosts
// prefer the default-route interface. If that cannot produce an IPv4 address,
// it falls back to the legacy interface scan to preserve behavior on hosts
// without usable route information.
func GetHostIP() (string, error) {
	ip, err := getDefaultHostIPv4()
	if err == nil {
		return ip, nil
	}

	return getFirstInterfaceIPv4()
}

func getDefaultHostIPv4() (string, error) {
	ip, err := netutils.ChooseHostInterface()
	if err != nil {
		return "", err
	}

	return ipv4String(ip)
}

func getFirstInterfaceIPv4() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	return getFirstIPv4FromAddrs(addrs)
}

func getFirstIPv4FromAddrs(addrs []net.Addr) (string, error) {
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if ok && !ipNet.IP.IsLoopback() {
			ip, err := ipv4String(ipNet.IP)
			if err == nil {
				return ip, nil
			}
		}
	}

	return "", fmt.Errorf("no valid host IP found")
}

func ipv4String(ip net.IP) (string, error) {
	ipv4 := ip.To4()
	if ipv4 == nil {
		return "", fmt.Errorf("no valid host IPv4 found")
	}

	return ipv4.String(), nil
}
