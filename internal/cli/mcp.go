package cli

import (
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/ivanrvpereira/agentmailguard-go/internal/client"
	"github.com/ivanrvpereira/agentmailguard-go/internal/mcptools"
)

// nopWriteCloser keeps the transport from closing the process output.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// mcpCmd serves the MCP tools on stdin and stdout until the client disconnects. It is a data command:
// every tool call goes to the server over HTTP, and stdout carries only protocol messages.
func mcpCmd(r *runner) *cobra.Command {
	var allowWrites bool
	c := r.leaf("mcp", "Serve MCP tools over stdio for an AI agent", 0, 0, func(c *cobra.Command, _ []string) error {
		_, base, err := r.resolve(c, false)
		if err != nil {
			return err
		}
		clientOpts := r.opts.Client
		clientOpts.Timeout = r.timeout
		cl, e := client.New(base, clientOpts)
		if e != nil {
			r.code = failure(r.stderr, "invalid server URL")
			return errDone
		}
		stdin := r.opts.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		s := mcptools.New(cl, mcptools.Options{Account: r.account, AllowWrites: allowWrites})
		// The SDK error may quote protocol input, so only a fixed line is shown. Cancellation is a clean stop.
		e = s.Run(c.Context(), &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{r.stdout}})
		if e != nil && c.Context().Err() == nil {
			r.code = failure(r.stderr, "mcp transport failed")
		}
		return errDone
	})
	c.Flags().BoolVar(&allowWrites, "allow-writes", false, "also serve the mail change and draft tools")
	return c
}
