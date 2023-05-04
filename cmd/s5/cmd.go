package s5

import (
	"os"
	"path/filepath"
	"time"

	"github.com/rkonfj/toh/cmd/s5/server"
	"github.com/rkonfj/toh/spec"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "s5",
		Short: "Socks5 proxy server act as ToH client",
		Args:  cobra.NoArgs,
		RunE:  startAction,
	}
	Cmd.Flags().StringP("config", "c", "", "socks5 server config file (default is $HOME/.config/toh/socks5.yml)")
	Cmd.Flags().String("dns", "", "dns upstream to use (leave blank to disable local dns)")
	Cmd.Flags().String("dns-listen", "0.0.0.0:2053", "local dns")
	Cmd.Flags().String("dns-evict", "2h", "local dns cache evict duration")
}

func startAction(cmd *cobra.Command, args []string) error {
	opts, err := processOptions(cmd)
	if err != nil {
		return err
	}
	sm, err := server.NewSocks5Server(opts)
	if err != nil {
		return err
	}
	return sm.Run()
}

func processOptions(cmd *cobra.Command) (opts server.Options, err error) {
	opts.DNSUpstream, err = cmd.Flags().GetString("dns")
	if err != nil {
		return
	}

	opts.DNSListen, err = cmd.Flags().GetString("dns-listen")
	if err != nil {
		return
	}

	dnsEvict, err := cmd.Flags().GetString("dns-evict")
	if err != nil {
		return
	}
	opts.DNSEvict, err = time.ParseDuration(dnsEvict)
	if err != nil {
		return
	}

	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	defer func() {
		datapath := filepath.Dir(configPath)
		if datapath == "." {
			opts.DataRoot = filepath.Join(homeDir, ".config", "toh")
			return
		}
		opts.DataRoot = datapath
	}()

	var configF *os.File
	if configPath != "" {
		configF, err = os.Open(configPath)
		if err != nil {
			return
		}
		opts.Cfg = server.Config{}
		err = yaml.NewDecoder(configF).Decode(&opts.Cfg)
		return
	}

	configPath = filepath.Join(homeDir, ".config", "toh", "socks5.yml")
	configF, err = os.Open(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return
		}
		logrus.Infof("initializing config file %s", configPath)
		err = os.MkdirAll(filepath.Join(homeDir, ".config", "toh"), 0755)
		if err != nil {
			return
		}
		configF, err = os.OpenFile(configPath, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		opts.Cfg = *defaultOptions()
		enc := yaml.NewEncoder(spec.NewConfigWriter(configF))
		enc.SetIndent(2)
		err = enc.Encode(opts.Cfg)
		return
	}
	opts.Cfg = server.Config{}
	err = yaml.NewDecoder(configF).Decode(&opts.Cfg)
	return
}

func defaultOptions() *server.Config {
	return &server.Config{
		Geoip2: "country.mmdb",
		Listen: "0.0.0.0:2080",
		Servers: []server.TohServer{{
			Name:        "us1",
			Api:         "wss://us-l4-vultr.synf.in/ws",
			Key:         "5868a941-3025-4c6d-ad3a-41e29bb42e5f",
			Ruleset:     []string{"https://raw.githubusercontent.com/rkonfj/toh/main/ruleset.txt"},
			Healthcheck: "https://www.google.com/generate_204",
		}},
	}
}
