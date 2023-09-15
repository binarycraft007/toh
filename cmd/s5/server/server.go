package server

import (
	"context"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/binarycraft007/toh/client"
	D "github.com/binarycraft007/toh/dns"
	"github.com/binarycraft007/toh/socks5"
	"github.com/miekg/dns"
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

	s5Server.dns = D.NewLocalDNS(D.Options{
		Listen:   opts.DNSListen,
		Upstream: opts.DNSUpstream,
		Evict:    opts.DNSEvict,
		Exchange: s5Server.dnsExchange,
	})

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

	go s.dns.Run()
	go s.localAddrFamilyDetection()
	return ss.Run()
}

func (s *S5Server) loadServer() (err error) {
	srv := s.opts.Cfg.Server
	var c *client.TohClient
	opts := client.Options{
		Server:     srv.Addr,
		Key:        srv.Key,
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
		httpClient: &http.Client{
			Timeout:   5 * time.Minute,
			Transport: &http.Transport{DialContext: c.DialContext},
		},
		httpIPv4: &http.Client{
			Timeout: 6 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return c.DialContext(ctx, "tcp4", addr)
				},
			},
		},
		httpIPv6: &http.Client{
			Timeout: 6 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return c.DialContext(ctx, "tcp6", addr)
				},
			},
		},
	}
	go s.server.healthcheck(srv.Healthcheck)
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

func (s *S5Server) dnsExchange(dnServer string, clientAddr string, r *dns.Msg) (
	resp *dns.Msg, err error) {
	if r.Question[0].Qtype == dns.TypeAAAA {
		if !s.server.ipv6Enabled() {
			resp = &dns.Msg{}
			resp.Question = r.Question
			resp.SetReply(&dns.Msg{})
			return
		}
	}
	if r.Question[0].Qtype == dns.TypeA {
		if !s.server.ipv4Enabled() {
			resp = &dns.Msg{}
			resp.Question = r.Question
			resp.SetReply(&dns.Msg{})
			return
		}
	}
	resp, err = s.server.client.DNSExchange(dnServer, r)
	if err != nil {
		return
	}
	return
}

func (s *S5Server) localAddrFamilyDetection() {
	if s.opts.Cfg.LocalNet == nil {
		return
	}
	if len(s.opts.Cfg.LocalNet.AddrFamilyDetectURL) == 0 {
		return
	}

	httpIPv4 := newHTTPClient(D.LookupIP4)
	httpIPv6 := newHTTPClient(D.LookupIP6)

	for {
		var err error
		for _, url := range s.opts.Cfg.LocalNet.AddrFamilyDetectURL {
			_, err = httpIPv4.Get(url)
			if err == nil {
				s.localNetIPv4 = true
				break
			}
		}
		if err != nil {
			s.localNetIPv4 = false
		}

		for _, url := range s.opts.Cfg.LocalNet.AddrFamilyDetectURL {
			_, err = httpIPv6.Get(url)
			if err == nil {
				s.localNetIPv6 = true
				break
			}
		}
		if err != nil {
			s.localNetIPv6 = false
		}

		time.Sleep(30 * time.Second)
	}
}

func newHTTPClient(lookupIP func(host string) (ips []net.IP, err error)) *http.Client {
	dialer := net.Dialer{}
	return &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			DialContext: func(
				ctx context.Context,
				network, addr string,
			) (c net.Conn, err error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return
				}
				ips, err := lookupIP(host)
				if err != nil {
					return
				}
				return dialer.DialContext(ctx, network,
					net.JoinHostPort(ips[rand.Intn(len(ips))].String(), port))
			},
		},
	}
}
