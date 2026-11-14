package selfupdate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReplaceBinary puts newBin at exePath: it is written to exePath+".new" (same directory, so the
// rename cannot cross filesystems), the current file is kept as exePath+".old" (a hard link, or a
// copy where links are not possible) and ".new" is renamed over exePath. The rename is atomic and,
// on Unix, safe while the old binary is running. Returns the path of the kept previous binary.
func ReplaceBinary(exePath string, newBin []byte) (string, error) {
	newPath, oldPath := exePath+".new", exePath+".old"
	if err := os.WriteFile(newPath, newBin, 0o755); err != nil {
		return "", fmt.Errorf("cannot write next to the binary (%s): %w", filepath.Dir(exePath), err)
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		_ = os.Remove(newPath)
		return "", err
	}
	kept := ""
	if _, err := os.Stat(exePath); err == nil {
		_ = os.Remove(oldPath)
		if err := os.Link(exePath, oldPath); err != nil {
			if err := copyFile(exePath, oldPath); err != nil {
				_ = os.Remove(newPath)
				return "", fmt.Errorf("keeping the previous binary: %w", err)
			}
		}
		kept = oldPath
	}
	if err := os.Rename(newPath, exePath); err != nil {
		_ = os.Remove(newPath)
		return "", err
	}
	return kept, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Updater checks for and installs new versions of one binary.
type Updater struct {
	Client  *Client
	Version string // the running version (main.version); "dev" is never updated
	Bin     string // "squatterd" or "tf-squatterd"
	ExePath string // the running binary's path
	GOOS    string
	GOARCH  string
}

// Result is the outcome of Check or Update.
type Result struct {
	Current string
	Latest  string // "" when nothing was found
	Newer   bool
	Applied bool
	Kept    string // the previous binary, after an update
}

// Check looks up the newest release for the channel and says whether it is newer than the running one.
// A "dev" (non-semver) running version is unknown: Newer stays false and no network call is made.
func (u *Updater) Check(ctx context.Context, channel string) (res Result, rel Release, err error) {
	res = Result{Current: u.Version}
	if _, perr := ParseVersion(u.Version); perr != nil {
		return res, Release{}, fmt.Errorf("this is a development build (version %q): it is never updated", u.Version)
	}
	rel, err = u.Client.Latest(ctx, channel)
	if err != nil {
		return res, rel, err
	}
	res.Latest = rel.Version()
	res.Newer = Newer(u.Version, rel.Tag)
	return res, rel, nil
}

// Update installs the release over the running binary.
func (u *Updater) Update(ctx context.Context, rel Release) (Result, error) {
	res := Result{Current: u.Version, Latest: rel.Version(), Newer: true}
	tgz, err := u.Client.Download(ctx, rel, u.GOOS, u.GOARCH)
	if err != nil {
		return res, err
	}
	bin, err := ExtractBinary(tgz, u.Bin)
	if err != nil {
		return res, err
	}
	kept, err := ReplaceBinary(u.ExePath, bin)
	if err != nil {
		return res, err
	}
	res.Applied, res.Kept = true, kept
	return res, nil
}
