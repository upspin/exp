// TODO(ehg) change copyright when this moves into main upspin.io repository.
/* Copyright 2019 Eric Grosse grosse@gmail.com.
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

// Upsync keeps a local disk copy in sync with a master version in Upspin.
// See the command's usage method for documentation.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"upspin.io/client"
	"upspin.io/cmd/cacheserver/cacheutil"
	"upspin.io/config"
	"upspin.io/flags"
	"upspin.io/transports"
	"upspin.io/upspin"
	"upspin.io/version"
)

var lastUpsync int64 // Unix time when an upsync was last completed

const help = `Upsync keeps a local disk copy in sync with a master version in
Upspin. It is a weak substitute for upspinfs.

To start, create a local directory whose path ends in a string that looks like
an existing upspin directory, such as ~/u/alice@example.com. Cd there and execute
upsync.  Make local edits to the downloaded files or create new files, and then
upsync to upload your changes to the Upspin master. To discard your local changes,
just remove the edited local files and upsync. (Executing both local rm and
upspin rm are required to remove content permanently.)

Upsync prints which files it is uploading or downloading and declines to download
files larger than 50MB. It promises never to write outside the starting directory
and subdirectories and, as an initial way to enforce that, declines all symlinks.

There are no clever merge heuristics;  copying back and forth proceeds by a trivial
"newest wins" rule.  This requires some discipline in remembering to upsync after
each editing session and is better suited to single person rather than joint
editing. Don't let your computer clocks drift.

With better FUSE support on Windows and OpenBSD it will be possible to switch
to the much preferable upspinfs. But even then upsync may have benefits:
* enables work offline, i.e. a workaround for (missing) distributed upspinfs
* offers mitigation of user misfortune, such as losing upspin keys
* provides a worked out example for new Upspin client developers
* leaves a backup in case cloud store or Upspin projects die without warning

This tool was written assuming you are an experienced Upspin user trying to
assist a friend with file sharing or backup on Windows 10.  Here is a checklist:
1. create or check existing upspin account and permissions
   It is helpful if you can provide them space on an existing server.
2. confirm \Users\x\upspin\config is correct
3. disk must be NTFS (because FAT has peculiar timestamps)
4. open a powershell window
5. install go and git, if not already there
6. go get -u upspin.io/cmd/...
7. fetch upsync.go; go install
   Go files must be transferred as UTF8, else expect a NUL compile warning.
8. mkdir \Users\alice\u...
9. upsync

`

const cmdName = "upsync"

var upsyncFlag = flag.String("upsync", upspinDir("upsync"), "file whose mtime is last upsync")

func usage() {
	fmt.Fprintln(os.Stderr, help)
	fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("upsync: ")
	flag.Usage = usage
	flags.Parse(flags.Client, "version")
	if flags.Version {
		fmt.Print(version.Version())
		return
	}
	if flag.NArg() > 0 {
		usage()
	}

	// setup Upspin client
	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		log.Fatal(err)
	}
	transports.Init(cfg)
	cacheutil.Start(cfg)
	upc := client.New(cfg)

	// guess at previous upsync time
	getwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	lastUpsyncFi, err := os.Stat(*upsyncFlag)
	if err == os.ErrNotExist { // first time
		// TODO  not working on Windows?!!!
		err = ioutil.WriteFile(*upsyncFlag, []byte(getwd), 0644)
		if err != nil {
			log.Fatal(err)
		}
	} else if err != nil { // stat failed; very unusual
		log.Fatal(err)
	} else { // normal case
		lastUpsync = lastUpsyncFi.ModTime().Unix()
	}
	log.Printf("lastUpsync %v", lastUpsyncFi.ModTime())

	// find first component of current directory that looks like email address
	// then make wd == upspin working directory
	wd := getwd
	i := strings.IndexByte(wd, '@')
	if i < 0 {
		log.Fatalln("couldn't find upspin user name in working directory")
	}
	i = strings.LastIndexAny(wd[:i], "\\/")
	if i < 0 {
		log.Fatalln("unable to parse working directory")
	}
	slash := wd[i : i+1]
	wd = wd[i+1:]
	if slash != "/" {
		wd = strings.ReplaceAll(wd, slash, "/")
	}

	// start copying
	upsync(upc, wd, "")

	// save time of this upsync for next upsync "skipping old" heuristic
	err = ioutil.WriteFile(*upsyncFlag, []byte(getwd), 0644)
	if err != nil {
		// We're more or less successful even if we can't record the time.  But warn.
		log.Fatal(err)
	}
}

// upsync walks the local and remote trees rooted at subdir to update each file to newer versions.
// The upspin.Client upc and the Upspin starting directory wd don't change from what was set in main.
// The subdir argument changes for the depth-first recursive tree walk.
func upsync(upc upspin.Client, wd, subdir string) {

	// udir and ldir are sorted lists of remote and local files in subdir.
	udir, err := upc.Glob(wd + "/" + subdir + "*")
	if err != nil {
		log.Fatal(err)
	}
	ldir, err := ioutil.ReadDir(subdir + ".")
	if err != nil {
		log.Fatal(err)
	}

	// Advance through the two lists, comparing at each iteration udir[uj] and ldir[lj].
	uj := 0
	lj := 0
	for {
		cmp := 0 // -1,0,1 as udir[uj] sorts before,same,after ldir[lj]
		if lj < len(ldir) && ldir[lj].Mode()&os.ModeSymlink != 0 {
			log.Fatalln("local symlinks are not allowed: " + ldir[lj].Name())
		}
		if uj >= len(udir) {
			if lj >= len(ldir) {
				break // both lists exhausted
			}
			cmp = 1
		} else if lj >= len(ldir) {
			cmp = -1
		} else {
			cmp = strings.Compare(string(udir[uj].SignedName)[len(wd)+1:], subdir+ldir[lj].Name())
		}

		// copy newer to older/missing
		switch cmp {
		case -1:
			pathname := string(udir[uj].SignedName)[len(wd)+1:]
			switch {
			case udir[uj].Attr&upspin.AttrLink != 0:
				fmt.Println("ignoring upspin symlink", pathname)
			case udir[uj].Attr&upspin.AttrDirectory != 0:
				err = os.Mkdir(pathname, 0700)
				if err != nil {
					log.Fatal(err)
				}
				upsync(upc, wd, pathname+"/")
			case udir[uj].Attr&upspin.AttrIncomplete != 0:
				fmt.Println("permission problem; creating placeholder ", pathname)
				empty := make([]byte, 0)
				err = ioutil.WriteFile(pathname, empty, 0)
				if err != nil {
					log.Fatal(err)
				}
			case len(udir[uj].Blocks) > 50:
				fmt.Println("skipping big", pathname)
			default:
				utime := int64(udir[uj].Time)
				pull(upc, wd, pathname, utime)
			}
			uj++
		case 0:
			pathname := subdir + ldir[lj].Name()
			uIsDir := udir[uj].Attr&upspin.AttrDirectory != 0
			lIsDir := ldir[lj].IsDir()
			if uIsDir != lIsDir {
				log.Fatalln("same name, different Directory attribute! " + pathname)
			}
			if uIsDir {
				upsync(upc, wd, pathname+"/")
			} else {
				utime := int64(udir[uj].Time)
				ltime := ldir[lj].ModTime().Unix()
				if utime > ltime {
					pull(upc, wd, pathname, utime)
				} else if utime < ltime {
					push(upc, wd, pathname, ltime)
				} // else assumed already in sync
			}
			uj++
			lj++
		case 1:
			pathname := subdir + ldir[lj].Name()
			if ldir[lj].IsDir() {
				fmt.Println("upspin mkdir", wd+"/"+pathname)
				_, err = upc.MakeDirectory(upspin.PathName(wd + "/" + pathname))
				if err != nil {
					log.Fatal(err)
				}
				upsync(upc, wd, pathname+"/")
			} else {
				ltime := ldir[lj].ModTime().Unix()
				push(upc, wd, pathname, ltime)
			}
			lj++
		}
	}
}

// pull copies pathname from Upspin to local disk, copying the modification time.
func pull(upc upspin.Client, wd, pathname string, utime int64) {
	fmt.Println("pull", pathname)
	bytes, err := upc.Get(upspin.PathName(wd + "/" + pathname))
	if err != nil {
		log.Fatal(err)
	}
	err = ioutil.WriteFile(pathname, bytes, 0600)
	if err != nil {
		log.Fatal(err)
	}
	mtime := time.Unix(utime, 0)
	err = os.Chtimes(pathname, mtime, mtime)
	if err != nil {
		log.Fatal(err)
	}
}

// pull copies pathname from local disk to Upspin, copying the modification time.
func push(upc upspin.Client, wd, pathname string, ltime int64) {
	if ltime < lastUpsync {
		fmt.Printf("skipping old %v %v\n", pathname, ltime)
		return
	}
	fmt.Println("push", pathname)
	bytes, err := ioutil.ReadFile(pathname)
	if err != nil {
		log.Fatal(err)
	}
	path := upspin.PathName(wd + "/" + pathname)
	_, err = upc.Put(path, bytes)
	if err != nil {
		log.Fatal(err)
	}
	err = upc.SetTime(path, upspin.Time(ltime))
	if err != nil {
		log.Fatal(err)
	}
}

// upspinDir is copied from upspin.io/flags/flags.go.
func upspinDir(subdir string) string {
	home, err := config.Homedir()
	if err != nil {
		log.Printf("upsync: could not locate home directory: %v", err)
		home = "."
	}
	return filepath.Join(home, "upspin", subdir)
}
