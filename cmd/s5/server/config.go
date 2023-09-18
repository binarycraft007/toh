package server

import (
	"net/http"
)

var (
	DefaultAddrFamilyDetectURL = []string{
		"http://detectportal.firefox.com/success.txt",
		"http://204.ustclug.org",
	}
	DefaultServerHealthcheck = []string{
		"http://www.google.com/generate_204",
		"http://maps.google.com/generate_204",
	}
)

type Config struct {
	// socks5+http proxy server listen addr
	Listen string `json:"listen"`
	// advertised server addr
	Advertise *Advertise `json:"advertise,omitempty"`
	// toh server list
	Server *TohServer `json:"server"`
	// local network settings
	LocalNet *LocalNet `json:"localnet,omitempty"`
}

// Advertise since the socks5 server can listen to multiple network cards or be reverse-proxyed
// we need to set an advertising ip and port
// for example, socks5 UDP ASSOCIATE refers to this address when responding to the client
type Advertise struct {
	IP   string `json:"ip,omitempty"`
	Port uint16 `json:"port,omitempty"`
}

type TohServer struct {
	// toh server authcate key
	Password   string `json:"password"`
	RemoteAddr string `json:"remote_addr"`
	RemotePort int    `json:"remote_port"`
	// server name indication
	ServerName string `json:"server_name,omitempty"`
	// url that responds to any http status code. dual stack IP should be supported
	Healthcheck []string `json:"healthcheck,omitempty"`
	// the interval send ping to the under websocket conn for keepalive
	Keepalive string `json:"keepalive,omitempty"`
	// customize the request header sent to the toh server
	Headers http.Header `json:"headers,omitempty"`
}

type LocalNet struct {
	// url that responds to any http status code. dual stack IP should be supported
	AddrFamilyDetectURL []string `json:"afdetect,omitempty"`
}

func (c *Config) applyDefaults() {
	if len(c.Server.Healthcheck) == 0 {
		c.Server.Healthcheck = DefaultServerHealthcheck
	}

	if c.LocalNet == nil {
		c.LocalNet = &LocalNet{}
	}

	if len(c.LocalNet.AddrFamilyDetectURL) == 0 {
		c.LocalNet.AddrFamilyDetectURL = DefaultAddrFamilyDetectURL
	}
}
