package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/binarycraft007/toh/client"
	D "github.com/binarycraft007/toh/dns"
	"github.com/binarycraft007/toh/socks5"
	"github.com/oschwald/geoip2-golang"
)

type Options struct {
	// config from file
	Cfg Config
	// socks5+http listen address (specify this to override from config)
	Listen string
	// data root directory. i.e. $HOME/.config/toh
	DataRoot string
	// when using socks5 proxy dns query, if the query dns is consistent with the fake ip
	// the query request will be processed by the built-in local dns
	DNSFake []string
	// build-in local dns listen address
	DNSListen string
	// build-in local dns used upstream dns
	DNSUpstream string
	// how often query results are completely removed from the cache
	DNSEvict time.Duration
}

type S5Server struct {
	opts                       Options
	socks5Opts                 socks5.Options
	server                     *Server
	defaultDialer              net.Dialer
	geoip2db                   *geoip2.Reader
	dns                        *D.LocalDNS
	localNetIPv4, localNetIPv6 bool
}

type Server struct {
	client      *client.TohClient
	httpIPv4    *http.Client
	httpIPv6    *http.Client
	httpClient  *http.Client
	latency     time.Duration
	latencyIPv6 time.Duration
}

func (s *Server) ipv6Enabled() bool {
	return s.latencyIPv6 < s.httpClient.Timeout
}

func (s *Server) ipv4Enabled() bool {
	return s.latency < s.httpClient.Timeout
}

func (s *Server) healthcheck(urls []string) {
	if len(urls) == 0 {
		s.latency = time.Duration(0)
		s.latencyIPv6 = time.Duration(0)
		return
	}
	for {
		var errIPv4, errIPv6 error
		for _, url := range urls {
			t1 := time.Now()
			_, errIPv4 = s.httpIPv4.Get(url)
			if errIPv4 == nil {
				s.latency = time.Since(t1)
				break
			}
		}
		if errIPv4 != nil {
			s.latency = s.httpClient.Timeout
		}

		for _, url := range urls {
			t2 := time.Now()
			_, errIPv6 = s.httpIPv6.Get(url)
			if errIPv6 == nil {
				s.latencyIPv6 = time.Since(t2)
				break
			}
		}
		if errIPv6 != nil {
			s.latencyIPv6 = s.httpClient.Timeout
		}

		time.Sleep(30 * time.Second)
	}
}

func NewS5Server(opts Options) (s5Server *S5Server, err error) {
	opts.Cfg.applyDefaults()
	s5Server = &S5Server{
		opts:          opts,
		server:        &Server{},
		defaultDialer: net.Dialer{},
		localNetIPv4:  true,
		localNetIPv6:  true,
	}

	s5Server.socks5Opts = socks5.Options{
		Listen:               opts.Cfg.Listen,
		TCPDialContext:       s5Server.dialTCP,
		UDPDialContext:       s5Server.dialUDP,
		TrafficEventConsumer: logTrafficEvent,
		HTTPHandlers:         make(map[string]socks5.Handler),
	}

	if opts.Cfg.Advertise != nil {
		s5Server.socks5Opts.AdvertiseIP = opts.Cfg.Advertise.IP
		s5Server.socks5Opts.AdvertisePort = opts.Cfg.Advertise.Port
	}

	// overwrite config from command line flag
	if len(opts.Listen) > 0 {
		s5Server.socks5Opts.Listen = opts.Listen
	}

	if err = s5Server.loadServer(); err != nil {
		return
	}

	return
}

func (s *S5Server) Run() error {
	ss, err := socks5.NewSocks5Server(s.socks5Opts)
	if err != nil {
		return err
	}

	return ss.Run()
}

func (s *S5Server) loadServer() (err error) {
	srv := s.opts.Cfg.Server
	var c *client.TohClient
	opts := client.Options{
		RemoteAddr: srv.RemoteAddr,
		RemotePort: srv.RemotePort,
		Password:   srv.Password,
		Headers:    srv.Headers,
		ServerName: srv.ServerName,
	}
	if len(srv.Keepalive) > 0 {
		opts.Keepalive, err = time.ParseDuration(srv.Keepalive)
		if err != nil {
			return
		}
	}
	c, err = client.NewTohClient(opts)
	if err != nil {
		return
	}
	s.server = &Server{
		client:      c,
		latency:     5 * time.Minute,
		latencyIPv6: 5 * time.Minute,
	}
	return
}

func (s *S5Server) dial(ctx context.Context, addr, network string) (
	conn net.Conn, err error) {
	conn, err = s.server.client.DialContext(ctx, network, addr)
	return
}

func (s *S5Server) dialTCP(ctx context.Context, addr string) (
	conn net.Conn, err error) {
	return s.dial(ctx, addr, "tcp")
}

func (s *S5Server) dialUDP(ctx context.Context, addr string) (
	conn net.Conn, err error) {
	return s.dial(ctx, addr, "udp")
}
