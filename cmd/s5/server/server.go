package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/oschwald/geoip2-golang"
	"github.com/rkonfj/toh/client"
	"github.com/rkonfj/toh/ruleset"
	"github.com/rkonfj/toh/server/api"
	"github.com/rkonfj/toh/socks5"
	"github.com/rkonfj/toh/spec"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
)

type Config struct {
	Geoip2  string        `yaml:"geoip2"`
	Listen  string        `yaml:"listen"`
	Servers []TohServer   `yaml:"servers"`
	Groups  []ServerGroup `yaml:"groups,omitempty"`
}

type Options struct {
	Cfg           Config
	AdvertiseIP   string
	AdvertisePort uint16
	DataRoot      string
	DNSFake       string
	DNSListen     string
	DNSUpstream   string
	DNSEvict      time.Duration
}

type TohServer struct {
	Name        string      `yaml:"name"`
	Addr        string      `yaml:"addr"`
	Key         string      `yaml:"key"`
	Ruleset     []string    `yaml:"ruleset,omitempty"`
	Healthcheck string      `yaml:"healthcheck,omitempty"`
	Keepalive   string      `yaml:"keepalive,omitempty"`
	Headers     http.Header `yaml:"headers,omitempty"`
}

type ServerGroup struct {
	Name    string   `yaml:"name"`
	Servers []string `yaml:"servers"`
	Ruleset []string `yaml:"ruleset"`
}

type S5Server struct {
	opts           Options
	socks5Opts     socks5.Options
	servers        []*Server
	groups         []*Group
	defaultDialer  net.Dialer
	geoip2db       *geoip2.Reader
	dnsClient      *dns.Client
	dnsCache       *dnsCache
	dnsCacheTicker *time.Ticker
}

type Server struct {
	name       string
	client     *client.TohClient
	httpClient *http.Client
	ruleset    *ruleset.Ruleset
	latency    time.Duration
	limit      *api.Stats
}

func (s *Server) limited() bool {
	if s.limit == nil {
		return false
	}
	if s.limit.Status == "" {
		return false
	}
	return s.limit.Status != "ok"
}

type Group struct {
	name    string
	servers []*Server
	ruleset *ruleset.Ruleset
}

func (g *Group) selectServer() *Server {
	return selectServer(g.servers)
}

func NewS5Server(opts Options) (s5Server *S5Server, err error) {
	s5Server = &S5Server{
		opts:          opts,
		servers:       []*Server{},
		groups:        []*Group{},
		defaultDialer: net.Dialer{},
		dnsClient:     &dns.Client{},
		dnsCache: &dnsCache{
			cache:     make(map[string]*cacheEntry),
			hostCache: make(map[string]*hostCacheEntry),
		},
		dnsCacheTicker: time.NewTicker(
			time.Duration(math.Max(float64(opts.DNSEvict/20), float64(time.Minute)))),
	}

	s5Server.socks5Opts = socks5.Options{
		Listen:               opts.Cfg.Listen,
		AdvertiseIP:          opts.AdvertiseIP,
		AdvertisePort:        opts.AdvertisePort,
		TCPDialContext:       s5Server.dialTCP,
		UDPDialContext:       s5Server.dialUDP,
		TrafficEventConsumer: logTrafficEvent,
		HTTPHandlers:         make(map[string]socks5.Handler),
	}

	err = s5Server.loadServers()
	if err != nil {
		return
	}

	err = s5Server.loadGroups()
	if err != nil {
		return
	}
	ruleset.ResetCache()
	s5Server.printRulesetStats()

	logrus.Infof("total loaded %d proxy servers and %d groups",
		len(s5Server.servers), len(s5Server.groups))

	s5Server.registerHTTPHandlers()
	return
}

func (s *S5Server) Run() error {
	ss, err := socks5.NewSocks5Server(s.socks5Opts)
	if err != nil {
		return err
	}

	go s.openGeoip2()
	go s.runDNSIfNeeded()
	return ss.Run()
}

func (s *S5Server) loadServers() (err error) {
	for _, srv := range s.opts.Cfg.Servers {
		var c *client.TohClient
		opts := client.Options{
			Server:  srv.Addr,
			Key:     srv.Key,
			Headers: srv.Headers,
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

		server := &Server{
			name:   srv.Name,
			client: c,
			httpClient: &http.Client{
				Timeout: 300 * time.Second,
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						return c.DialTCP(ctx, addr)
					},
				},
			},
		}
		if srv.Ruleset != nil {
			server.ruleset, err = ruleset.Parse(srv.Name, s.opts.DataRoot, srv.Ruleset, c.DialContext)
			if err != nil {
				return
			}
		}
		go healthcheck(server, srv.Healthcheck)
		go updateStats(server)
		s.servers = append(s.servers, server)
	}
	return
}

func (s *S5Server) loadGroups() (err error) {
	for _, g := range s.opts.Cfg.Groups {
		group := &Group{
			name:    g.Name,
			servers: []*Server{},
		}
		for _, s := range s.servers {
			if slices.Contains(g.Servers, s.name) {
				group.servers = append(group.servers, s)
			}
		}
		if len(group.servers) == 0 {
			continue
		}
		if g.Ruleset != nil {
			group.ruleset, err = ruleset.Parse(g.Name, s.opts.DataRoot, g.Ruleset,
				selectServer(group.servers).client.DialContext)
			if err != nil {
				return
			}
		}
		s.groups = append(s.groups, group)
	}
	return
}

func (s *S5Server) printRulesetStats() {
	for _, s := range s.servers {
		if s.ruleset != nil {
			s.ruleset.PrintStats()
		}
	}
	for _, g := range s.groups {
		if g.ruleset != nil {
			g.ruleset.PrintStats()
		}
	}
}

func (s *S5Server) registerHTTPHandlers() {
	s.socks5Opts.HTTPHandlers["/servers"] = s.listServers
	s.socks5Opts.HTTPHandlers["/groups"] = s.listGroups
	s.socks5Opts.HTTPHandlers["/outbound"] = s.outbound
}

func (s *S5Server) dial(ctx context.Context, addr, network string) (
	dialerName string, conn net.Conn, err error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}

	proxy := s.selectProxyServer(host)
	if proxy.err != nil {
		return
	}

	log := logrus.WithField(spec.AppAddr.String(), ctx.Value(spec.AppAddr))
	if proxy.ok() {
		dialerName = proxy.server.name
		access := addr
		if proxy.reverseResolutionHost != nil {
			access = fmt.Sprintf("%s:%s", *proxy.reverseResolutionHost, port)
		}
		log.Infof("%s://%s using %s latency %s", network, access, proxy.id(), proxy.server.latency)
		if network == "tcp" {
			conn, err = proxy.server.client.DialTCP(ctx, addr)
		} else if network == "udp" {
			conn, err = proxy.server.client.DialUDP(ctx, addr)
		} else {
			err = errors.New("unsupported network " + network)
		}
		return
	}

	log.Infof("%s://%s using direct", network, addr)
	dialerName = "direct"
	conn, err = s.defaultDialer.DialContext(ctx, network, addr)
	return
}

func (s *S5Server) dialTCP(ctx context.Context, addr string) (
	dialerName string, conn net.Conn, err error) {
	return s.dial(ctx, addr, "tcp")
}

func (s *S5Server) dialUDP(ctx context.Context, addr string) (
	dialerName string, conn net.Conn, err error) {
	if len(s.opts.DNSUpstream) > 0 && strings.Contains(addr, s.opts.DNSFake) {
		dialerName = "direct"
		conn, err = s.defaultDialer.Dial("udp", s.opts.DNSListen)
		return
	}
	return s.dial(ctx, addr, "udp")
}

func (s *S5Server) openGeoip2() {
	geoip2Path := s.opts.Cfg.Geoip2
	if !filepath.IsAbs(geoip2Path) {
		geoip2Path = filepath.Join(s.opts.DataRoot, geoip2Path)
	}

	db, err := geoip2.Open(geoip2Path)
	if err != nil {
		if strings.Contains(err.Error(), "invalid MaxMind") {
			os.Remove(geoip2Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			logrus.Errorf("geoip2 open faild: %s", err.Error())
			return
		}
		downloadGeoip2DB(selectServer(s.servers).httpClient, geoip2Path)
		s.openGeoip2()
		return
	}
	s.geoip2db = db
}

func selectServer(servers []*Server) *Server {
	var s []*Server
	for _, server := range servers {
		if server.limited() {
			continue
		}
		s = append(s, server)
	}
	sort.Slice(s, func(i, j int) bool {
		return s[i].latency < s[j].latency
	})
	if len(s) == 0 {
		return servers[0]
	}
	return s[0]
}

func downloadGeoip2DB(hc *http.Client, geoip2Path string) {
	logrus.Infof("downloading %s (this can take up to %s)", geoip2Path, hc.Timeout)
	mmdbURL := "https://github.com/Dreamacro/maxmind-geoip/releases/latest/download/Country.mmdb"
	resp, err := hc.Get(mmdbURL)
	if err != nil {
		logrus.Error(err)
		return
	}
	defer resp.Body.Close()
	mmdb, err := os.OpenFile(geoip2Path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logrus.Errorf("open db %s error: %s", geoip2Path, err)
		return
	}
	defer mmdb.Close()
	_, err = io.Copy(mmdb, resp.Body)
	if err != nil {
		logrus.Error("download country.mmdb error: ", err)
		return
	}
	logrus.Info("download country.mmdb successfully")
}
