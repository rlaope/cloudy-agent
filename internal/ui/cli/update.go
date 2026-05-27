package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/rlaope/cloudy/internal/selfupdate"
)

func init() { Register(&updateCmd{}) }

type updateCmd struct{}

func (updateCmd) Name() string  { return "update" }
func (updateCmd) Short() string { return "upgrade cloudy to the latest GitHub release" }

func (updateCmd) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		return errf("update: unexpected argument %q (no flags accepted)", args[0])
	}
	res, err := selfupdate.Run(ctx, stdout)
	if err != nil {
		return errf("update: %w", err)
	}
	if res.Replaced {
		fmt.Fprintln(stdout, "\nrestart cloudy or open a new shell to use the new version.")
	}
	return nil
}
