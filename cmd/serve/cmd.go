package serve

import (
	"github.com/dustin/go-humanize"
	"github.com/rkonfj/toh/server"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "serve",
		Short: "ToH server daemon",
		Args:  cobra.NoArgs,
		RunE:  startAction,
	}
	Cmd.Flags().String("acl", "acl.json", "file containing access control rules")
	Cmd.Flags().String("admin", "", "admin key (leave blank to disable admin api)")
	Cmd.Flags().String("copy-buf", "16Ki", "buffer size for copying network data")
	Cmd.Flags().StringP("listen", "l", "127.0.0.1:9986", "http server listen address")
}

func startAction(cmd *cobra.Command, args []string) error {
	options, err := processServerOptions(cmd)
	if err != nil {
		return err
	}
	s, err := server.NewTohServer(options)
	if err != nil {
		return err
	}
	s.Run()
	return nil
}

func processServerOptions(cmd *cobra.Command) (options server.Options, err error) {
	options.Listen, err = cmd.Flags().GetString("listen")
	if err != nil {
		return
	}
	options.ACL, err = cmd.Flags().GetString("acl")
	if err != nil {
		return
	}
	options.Admin, err = cmd.Flags().GetString("admin")
	if err != nil {
		return
	}
	copyBuf, err := cmd.Flags().GetString("copy-buf")
	if err != nil {
		return
	}
	options.Buf, err = humanize.ParseBytes(copyBuf)
	return
}
