package server

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/geoip2-golang"
	"github.com/rkonfj/toh/client"
	"github.com/rkonfj/toh/cmd/s5/ruleset"
	"github.com/rkonfj/toh/socks5"
	"github.com/rkonfj/toh/spec"
	"github.com/sirupsen/logrus"
)

type Config struct {
	Geoip2  string      `yaml:"geoip2"`
	Listen  string      `yaml:"listen"`
	Servers []TohServer `yaml:"servers"`
}

type TohServer struct {
	Name    string   `yaml:"name"`
	Api     string   `yaml:"api"`
	Key     string   `yaml:"key"`
	Ruleset []string `yaml:"ruleset"`
}

type RulebasedSocks5Server struct {
	cfg           Config
	servers       []*ToH
	defaultDialer net.Dialer
	geoip2db      *geoip2.Reader
}

type ToH struct {
	name    string
	client  *client.TohClient
	ruleset *ruleset.Ruleset
}

func NewSocks5Server(dataPath string, cfg Config) (*RulebasedSocks5Server, error) {
	var servers []*ToH
	for _, s := range cfg.Servers {
		c, err := client.NewTohClient(client.Options{
			ServerAddr: s.Api,
			ApiKey:     s.Key,
		})
		if err != nil {
			return nil, err
		}

		rs, err := ruleset.Parse(c, s.Name, s.Ruleset, dataPath)
		if err != nil {
			return nil, err
		}
		server := &ToH{
			name:    s.Name,
			client:  c,
			ruleset: rs,
		}
		servers = append(servers, server)
	}
	httpClient := securityHttpClient(servers)
	db, err := openGeoip2(httpClient, dataPath, cfg.Geoip2)
	if err != nil {
		return nil, err
	}
	return &RulebasedSocks5Server{
		cfg:           cfg,
		servers:       servers,
		defaultDialer: net.Dialer{},
		geoip2db:      db,
	}, nil
}

func (s *RulebasedSocks5Server) Run() error {
	ss := socks5.NewSocks5Server(socks5.Options{
		Listen:               s.cfg.Listen,
		TCPDialContext:       s.dialTCP,
		UDPDialContext:       s.dialUDP,
		TrafficEventConsumer: logTrafficEvent,
	})
	return ss.Run()
}

func (s *RulebasedSocks5Server) dialTCP(ctx context.Context, addr string) (dialerName string, conn net.Conn, err error) {
	log := logrus.WithField(spec.AppAddr.String(), ctx.Value(spec.AppAddr))
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}

	ip := net.ParseIP(host)
	if ip != nil {
		c, _err := s.geoip2db.Country(ip)
		if _err != nil {
			err = _err
			return
		}

		if len(c.Country.IsoCode) == 0 {
			goto direct
		}

		for _, toh := range s.servers {
			if toh.ruleset.CountryMatch(c.Country.IsoCode) {
				log.Infof("%s using %s", addr, toh.name)
				dialerName = toh.name
				conn, err = toh.client.DialTCP(ctx, addr)
				return
			}
		}

		goto direct
	}

	for _, toh := range s.servers {
		if toh.ruleset.SpecialMatch(host) {
			log.Infof("%s using %s", addr, toh.name)
			dialerName = toh.name
			conn, err = toh.client.DialTCP(ctx, addr)
			return
		}
	}

	for _, toh := range s.servers {
		if toh.ruleset.WildcardMatch(host) {
			log.Infof("%s using %s", addr, toh.name)
			dialerName = toh.name
			conn, err = toh.client.DialTCP(ctx, addr)
			return
		}
	}

direct:
	log.Infof("%s using direct", addr)
	dialerName = "direct"
	conn, err = s.defaultDialer.DialContext(ctx, "tcp", addr)
	return
}

func (s *RulebasedSocks5Server) dialUDP(ctx context.Context, addr string) (dialerName string, conn net.Conn, err error) {
	toh := selectServer(s.servers)
	dialerName = toh.name
	conn, err = toh.client.DialUDP(ctx, addr)
	return
}

func selectServer(servers []*ToH) *ToH {
	return servers[rand.Intn(len(servers))]
}

func securityHttpClient(servers []*ToH) *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				server := selectServer(servers)
				addr, err := spec.ResolveIP(ctx, server.client.DialTCP, addr)
				if err != nil {
					return nil, err
				}
				return server.client.DialTCP(ctx, addr)
			},
		},
	}
}

func openGeoip2(httpClient *http.Client, dataPath, geoip2Path string) (*geoip2.Reader, error) {
	db, err := geoip2.Open(getGeoip2Path(httpClient, dataPath, geoip2Path))
	if err != nil {
		if strings.Contains(err.Error(), "invalid MaxMind") {
			logrus.Info("removed invalid country.mmdb file")
			os.Remove(geoip2Path)
			return openGeoip2(httpClient, dataPath,
				getGeoip2Path(httpClient, dataPath, ""))
		}
		if errors.Is(err, os.ErrNotExist) {
			return openGeoip2(httpClient, dataPath,
				getGeoip2Path(httpClient, dataPath, ""))
		}
		return nil, err
	}
	return db, nil
}

func getGeoip2Path(hc *http.Client, dataPath, geoip2Path string) string {
	if geoip2Path != "" {
		if filepath.IsAbs(geoip2Path) {
			return geoip2Path
		}
		return filepath.Join(dataPath, geoip2Path)
	}
	logrus.Infof("downloading country.mmdb to %s. this can take up to 2m0s", dataPath)
	mmdbPath := filepath.Join(dataPath, "country.mmdb")
	resp, err := hc.Get("https://github.com/Dreamacro/maxmind-geoip/releases/latest/download/Country.mmdb")
	if err != nil {
		logrus.Error("download error: ", err)
		return mmdbPath
	}
	defer resp.Body.Close()
	mmdb, err := os.OpenFile(mmdbPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logrus.Errorf("open db %s error: %s", mmdbPath, err)
		return mmdbPath
	}
	defer mmdb.Close()
	io.Copy(mmdb, resp.Body)
	return mmdbPath
}