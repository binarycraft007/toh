package s5

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/binarycraft007/toh/cmd/s5/server"
	"github.com/binarycraft007/toh/spec"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "s5",
		Short: "Socks5+http proxy server act as ToH client",
		Args:  cobra.NoArgs,
		RunE:  startAction,
	}
	Cmd.Flags().StringP("config", "c", "", "config file (default is $HOME/.config/toh/socks5.json)")
	Cmd.Flags().StringP("listen", "l", "", "socks5+http listen address (for override config file)")
	Cmd.Flags().String("dns", "", "local dns upstream (leave blank to disable local dns)")
	Cmd.Flags().String("dns-listen", "127.0.0.1:2053", "local dns listen address")
	Cmd.Flags().String("dns-evict", "2h", "local dns cache evict duration")
	Cmd.Flags().StringSlice("dns-fake", []string{}, "local fake dns (leave blank to disable fake dns)")
}

func startAction(cmd *cobra.Command, args []string) error {
	opts, err := processOptions(cmd)
	if err != nil {
		return err
	}
	sm, err := server.NewS5Server(opts)
	if err != nil {
		return err
	}
	return sm.Run()
}

func processOptions(cmd *cobra.Command) (opts server.Options, err error) {
	opts.Listen, err = cmd.Flags().GetString("listen")
	if err != nil {
		return
	}
	opts.DNSUpstream, err = cmd.Flags().GetString("dns")
	if err != nil {
		return
	}

	opts.DNSListen, err = cmd.Flags().GetString("dns-listen")
	if err != nil {
		return
	}

	opts.DNSFake, err = cmd.Flags().GetStringSlice("dns-fake")
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
		err = json.NewDecoder(configF).Decode(&opts.Cfg)
		return
	}

	configPath = filepath.Join(homeDir, ".config", "toh", "socks5.json")
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
		enc := json.NewEncoder(spec.NewConfigWriter(configF))
		enc.SetIndent("", "  ")
		err = enc.Encode(opts.Cfg)
		return
	}
	opts.Cfg = server.Config{}
	err = json.NewDecoder(configF).Decode(&opts.Cfg)
	return
}

func defaultOptions() *server.Config {
	return &server.Config{
		Listen: "127.0.0.1:2080",
		Server: &server.TohServer{
			ServerName: "fill-in-your-server-here.toh.sh",
			RemoteAddr: "fill-in-your-server-here.toh.sh",
			RemotePort: 443,
			Password:   "112qcPA4xPxh7PQV3fyTMEkfByEEn84EjNeMmskVTBVy2aCa4ipX",
		},
	}
}
