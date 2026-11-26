package pipmix

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"
)

type versionFlags struct {
	version bool
	info    bool
}

func addVersionFlags(fs *flag.FlagSet) *versionFlags {
	v := &versionFlags{}
	fs.BoolVar(&v.version, "version", false, "print the version and exit")
	fs.BoolVar(&v.info, "version.info", false, "print the version, Go build info and module versions, and exit")
	return v
}

// print writes what the set flags ask for to w and reports whether any was set.
func (v *versionFlags) print(w io.Writer) bool {
	if !v.version && !v.info {
		return false
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(w, "pipmix (unknown version)")
		return true
	}
	fmt.Fprintln(w, "pipmix", bi.Main.Version)
	if v.info {
		fmt.Fprint(w, bi)
	}
	return true
}
