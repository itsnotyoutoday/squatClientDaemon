package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// RunUpdate is the shared body of "<program> update [--check]": it prints what it finds and does,
// installs unless checkOnly, and returns the result so the caller can do program-specific follow-ups
// (squatterd reloads itself). A running version that is "dev" is reported and never touched.
func (u *Updater) RunUpdate(ctx context.Context, channel string, checkOnly bool, w io.Writer) (Result, error) {
	res, rel, err := u.Check(ctx, channel)
	switch {
	case errors.Is(err, ErrNoRelease):
		fmt.Fprintf(w, "no %s release found yet; nothing to update to\n", channel)
		return res, nil
	case err != nil:
		return res, err
	}
	if !res.Newer {
		fmt.Fprintf(w, "%s %s is up to date (newest %s release: %s)\n", u.Bin, u.Version, channel, res.Latest)
		return res, nil
	}
	if checkOnly {
		fmt.Fprintf(w, "update available: %s %s -> %s (%s channel). Run: %s update\n", u.Bin, u.Version, res.Latest, channel, u.Bin)
		return res, nil
	}
	fmt.Fprintf(w, "updating %s %s -> %s ...\n", u.Bin, u.Version, res.Latest)
	res, err = u.Update(ctx, rel)
	if err != nil {
		return res, err
	}
	fmt.Fprintf(w, "installed %s %s at %s (previous version kept as %s)\n", u.Bin, res.Latest, u.ExePath, res.Kept)
	return res, nil
}
