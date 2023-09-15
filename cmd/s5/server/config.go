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
	// maxmind geoip2 db path
	Geoip2 string `json:"geoip2,omitempty"`
	// socks5+http proxy server listen addr
	Listen string `json:"listen"`
	// advertised server addr
	Advertise *Advertise `json:"advertise,omitempty"`
	// toh server list
	Servers []*TohServer `json:"servers"`
	// group toh servers
	Groups []*ServerGroup `json:"groups,omitempty"`
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
	// name to identify the toh server
	Name string `json:"name"`
	// toh server adderss. i.e. https://fill-in-your-server-here.toh.sh/ws
	Addr string `json:"addr"`
	// toh server authcate key
	Key string `json:"key"`
	// this server is used when the remote accessed by the user hits this ruleset
	Ruleset []string `json:"ruleset,omitempty"`
	// url that responds to any http status code. dual stack IP should be supported
	Healthcheck []string `json:"healthcheck,omitempty"`
	// the interval send ping to the under websocket conn for keepalive
	Keepalive string `json:"keepalive,omitempty"`
	// customize the request header sent to the toh server
	Headers http.Header `json:"headers,omitempty"`
}

type ServerGroup struct {
	// name to identify the server group
	Name string `json:"name"`
	// toh server name list from `servers` section
	Servers []string `json:"servers"`
	// same as `servers` section
	Ruleset []string `json:"ruleset"`
	// loadbalancer rule. Round Robin (rr) or Best Latency (bl), default is bl
	Loadbalancer string `json:"loadbalancer"`
}

type LocalNet struct {
	// url that responds to any http status code. dual stack IP should be supported
	AddrFamilyDetectURL []string `json:"afdetect,omitempty"`
}

func (c *Config) applyDefaults() {
	if len(c.Geoip2) == 0 {
		c.Geoip2 = "country.mmdb"
	}

	for _, server := range c.Servers {
		if len(server.Healthcheck) == 0 {
			server.Healthcheck = DefaultServerHealthcheck
		}
	}

	if c.LocalNet == nil {
		c.LocalNet = &LocalNet{}
	}

	if len(c.LocalNet.AddrFamilyDetectURL) == 0 {
		c.LocalNet.AddrFamilyDetectURL = DefaultAddrFamilyDetectURL
	}
}
