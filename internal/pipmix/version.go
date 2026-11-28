package pipmix

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
)

type versionFlags struct {
	version cliBool
	info    cliBool
}

func addVersionFlags(fs *flag.FlagSet) *versionFlags {
	v := &versionFlags{}
	fs.Var(&v.version, "version", "print the version and exit; command line only")
	fs.Var(&v.info, "version.info", "print the version, Go build info and module versions, and exit; command line only")
	return v
}

// lock makes the flags ignore the environment and the config file, which
// are read after the command line.
func (v *versionFlags) lock() { v.version.locked, v.info.locked = true, true }

// print writes what the set flags ask for to w and reports whether any was set.
func (v *versionFlags) print(w io.Writer) bool {
	if !v.version.v && !v.info.v {
		return false
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(w, "pipmix (unknown version)")
		return true
	}
	fmt.Fprintln(w, "pipmix", bi.Main.Version)
	if v.info.v {
		fmt.Fprint(w, bi)
	}
	return true
}

// A cliBool is a bool flag that ignores Set once locked.
type cliBool struct {
	v      bool
	locked bool
}

func (b *cliBool) String() string { return strconv.FormatBool(b.v) }

func (b *cliBool) Set(s string) error {
	if b.locked {
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	b.v = v
	return nil
}

func (b *cliBool) IsBoolFlag() bool { return true }
