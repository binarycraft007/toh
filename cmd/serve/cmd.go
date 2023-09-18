package serve

import (
	"github.com/binarycraft007/toh/server"
	"github.com/dustin/go-humanize"
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
	Cmd.Flags().String("admin-key", "", "key to access the admin api (leave blank to disable admin api)")
	Cmd.Flags().String("copy-buf", "32ki", "buffer size for copying network data")
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
	options.ACL, err = cmd.Flags().GetString("acl")
	if err != nil {
		return
	}
	options.AdminKey, err = cmd.Flags().GetString("admin-key")
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
