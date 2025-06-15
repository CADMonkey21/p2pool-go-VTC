package util

import (
	"io/ioutil"
	"net"
	"net/http"
	"time"
)

// GetPublicIP fetches the public IP address from an external service.
func GetPublicIP() (net.IP, error) {
	client := http.Client{
		Timeout: time.Second * 5,
	}
	// We use ipify.org as a simple, reliable service for this.
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	ipBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return net.ParseIP(string(ipBytes)), nil
}
