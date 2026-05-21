package util

import (
	"fmt"
	"net"

	netutils "k8s.io/apimachinery/pkg/util/net"
)

func GetHostIP() (string, error) {
	return getHostIP(netutils.ChooseHostInterface, net.InterfaceAddrs)
}

func getHostIP(chooseHostInterface func() (net.IP, error), interfaceAddrs func() ([]net.Addr, error)) (string, error) {
	ip, err := chooseHostInterface()
	if err == nil {
		ipv4 := ip.To4()
		if ipv4 == nil {
			return "", fmt.Errorf("no valid host IPv4 found")
		}
		return ipv4.String(), nil
	}

	return getHostIPFromInterfaceAddrs(interfaceAddrs)
}

func getHostIPFromInterfaceAddrs(interfaceAddrs func() ([]net.Addr, error)) (string, error) {
	addrs, err := interfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no valid host IP found")
}
