// Command passage is Passage for Windows: the proxy in the notification
// area, with a window like the Android app's. It is portable: the settings
// (passage.ini) and the link (link.txt, one link per line) live next to the
// program, and a new link.txt is picked up while it runs.
//
//	passage.exe              # the window opens
//	passage.exe -autostart   # started at sign-in: tray only
//
// Elsewhere it serves the same window on loopback, for development.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// version is set by scripts/build-cli.sh.
var version = "dev"

//go:embed ui/index.html
var indexHTML string

func main() {
	dir := flag.String("dir", "", "settings folder (default: next to the program)")
	autostart := flag.Bool("autostart", false, "started at sign-in: no window")
	flag.Parse()
	d, err := portableDir(*dir)
	if err != nil {
		fatal(err)
	}
	runDesktop(d, *autostart)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "passage:", err)
	showError(err.Error())
	os.Exit(1)
}

// irohHelper is passage-iroh next to this program, or "" if there is none.
func irohHelper() string {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return ""
	}
	path := filepath.Join(filepath.Dir(exe), "passage-iroh"+exeSuffix)
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return ""
	}
	return path
}
