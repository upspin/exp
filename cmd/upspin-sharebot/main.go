// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command upspin-sharebot watches the root for the user in the provided config,
// detecting Access changes and re-wrapping any files whose reader set changed.
package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"upspin.io/access"
	"upspin.io/bind"
	"upspin.io/client"
	"upspin.io/config"
	"upspin.io/errors"
	"upspin.io/factotum"
	"upspin.io/flags"
	"upspin.io/log"
	"upspin.io/pack"
	"upspin.io/path"
	"upspin.io/shutdown"
	"upspin.io/upspin"

	_ "upspin.io/transports"
)

func main() {
	flags.Parse(flags.Client)

	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		log.Fatal(err)
	}
	w, err := NewWatcher(cfg)
	if err != nil {
		log.Fatal(err)
	}
	shutdown.Handle(w.Shutdown)
	select {}
}

// Watcher monitors a user root for Access file changes and re-wraps the keys
// for each file whose set of readers is affected by the change.
type Watcher struct {
	cfg upspin.Config
	dir upspin.DirServer
	key upspin.KeyServer

	seq int64 // owned by watch

	buffer   chan upspin.PathName
	check    chan upspin.PathName
	shutdown chan struct{} // closed to signal shutdown
	done     chan struct{} // closed when checkLoop exits

	mu sync.Mutex
	s  *Sharer
}

// NewWatcher initializes, starts, and returns a new Watcher for the user in
// the provided config.
func NewWatcher(cfg upspin.Config) (*Watcher, error) {
	if cfg.Factotum() == nil {
		return nil, errors.Str("no factotum in config")
	}
	dir, err := bind.DirServer(cfg, cfg.DirEndpoint())
	if err != nil {
		return nil, err
	}
	key, err := bind.KeyServer(cfg, cfg.KeyEndpoint())
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		cfg: cfg,
		dir: dir,
		key: key,

		seq: upspin.WatchCurrent,

		buffer:   make(chan upspin.PathName),
		check:    make(chan upspin.PathName),
		shutdown: make(chan struct{}),
		done:     make(chan struct{}),

		s: newSharer(cfg, dir, key),
	}
	go w.bufferLoop()
	go w.checkLoop()
	go w.watchLoop()
	return w, nil
}

// bufferLoop receives path names from buffer and sends them to check,
// buffering and de-duplicating them in between.
func (w *Watcher) bufferLoop() {
	defer close(w.check)
	files := make(map[upspin.PathName]bool)
	for {
		var name upspin.PathName
		var check chan upspin.PathName
		if len(files) > 0 {
			// Pick one entry at random from the files map.
			for name = range files {
				break
			}
			check = w.check
		}
		select {
		case check <- name:
			delete(files, name)
		case newName, active := <-w.buffer:
			if !active {
				return
			}
			files[newName] = true
		case <-w.shutdown:
			return
		}
	}
}

// checkLoop receives path names from check, inspects each for inconsistencies
// between readers and wrapped keys, and fixes them if found.
func (w *Watcher) checkLoop() {
	defer close(w.done)
	for name := range w.check {
		e, err := w.dir.Lookup(name)
		if errors.Is(errors.NotExist, err) {
			log.Debug.Printf("watcher: %v: no longer exists; skipping", name)
			continue
		}
		if err != nil {
			log.Error.Print(err)
			continue
		}
		if e.Packing != upspin.EEPack {
			log.Debug.Printf("watcher: %v: unknown packing %v", e.Name, e.Packing)
			continue
		}
		w.mu.Lock()
		readers, keyUsers, self, err := w.s.readers(e)
		w.mu.Unlock()
		if err != nil {
			log.Error.Print("watcher: ", err)
			continue
		}
		msg := fmt.Sprintf("%v self=%v\n\treaders: %v\n\tkeys: %v", e.Name, self, readers, keyUsers)
		if !self && readers.String() == keyUsers.String() {
			log.Debug.Print("watcher: ", msg)
			continue
		}
		log.Info.Printf("watcher: fixing inconsistency: %v", msg)
		w.mu.Lock()
		if err := w.s.fixShare(e, readers); err != nil {
			log.Error.Print("watcher: ", err)
		}
		w.mu.Unlock()
	}
}

// watchLoop watches the user root, retrying if a watch fails.
func (w *Watcher) watchLoop() {
	for {
		dialed := time.Now()
		if err := w.watch(); err != nil {
			log.Error.Printf("watcher: %v", err)
		}
		select {
		case <-w.shutdown:
			return
		default:
		}
		// Wait a minute between watches.
		const wait = 1 + time.Minute
		if elapsed := time.Since(dialed); elapsed < wait {
			time.Sleep(wait - elapsed)
		}
	}
}

// watch watches the user root for new files.
// When it sees an Access file it passes it to addAccess.
// Otherwise it sends the file's name to buffer.
func (w *Watcher) watch() error {
	var (
		name = upspin.PathName(w.cfg.UserName() + "/")
		done = make(chan struct{})
	)
	events, err := w.dir.Watch(name, w.seq, done)
	if err != nil {
		return err
	}
	for {
		log.Debug.Print("watcher: waiting for event")
		var e upspin.Event
		var ok bool
		select {
		case e, ok = <-events:
		case <-w.shutdown:
			return nil
		}
		if !ok {
			return nil
		}
		if e.Error != nil {
			return err
		}
		log.Debug.Printf("watcher: received event: %v delete=%t seq=%d", e.Entry.Name, e.Delete, e.Entry.Sequence)
		w.seq = e.Entry.Sequence
		if e.Entry.IsDir() {
			continue
		}
		if access.IsAccessFile(e.Entry.Name) {
			w.mu.Lock()
			if e.Delete {
				log.Debug.Printf("watcher: removeAccess: %v", e.Entry.Name)
				w.s.removeAccess(e.Entry.Name)
			} else {
				log.Debug.Printf("watcher: addAccess: %v", e.Entry.Name)
				if err := w.s.addAccess(e.Entry.Name); err != nil {
					log.Error.Print("watcher: ", err)
				}
			}
			w.mu.Unlock()

			p, err := path.Parse(e.Entry.Name)
			if err != nil {
				log.Error.Print("watcher: ", err)
				continue
			}
			go w.checkDir(p.Drop(1).Path())
			continue
		}
		if e.Delete {
			continue
		}
		select {
		case <-w.shutdown:
			return nil
		case w.buffer <- e.Entry.Name:
		}
	}
}

// checkDir recursively walks the given directory and sends each file to
// buffer. It will not descend into a directory that contains an Access file.
func (w *Watcher) checkDir(dir upspin.PathName) {
	des, err := w.dir.Glob(upspin.AllFilesGlob(dir))
	if err != nil {
		log.Print("watcher: ", err)
		return
	}
	for _, e := range des {
		if access.IsAccessFile(e.Name) {
			continue
		}
		if e.IsDir() {
			// If there's no Access file in the
			// directory then descend into it.
			accessFile := path.Join(e.Name, "Access")
			_, err := w.dir.Lookup(accessFile)
			if errors.Is(errors.NotExist, err) {
				w.checkDir(e.Name)
			}
			continue
		}
		select {
		case w.buffer <- e.Name:
		case <-w.shutdown:
			return
		}
	}
}

func (w *Watcher) Shutdown() {
	log.Debug.Print("watcher: shutting down")
	close(w.shutdown)
	<-w.done
	log.Debug.Print("watcher: shutdown complete")
}

// Sharer holds the state for the share calculation. It holds some caches to
// avoid calling on the server too much.
// TODO(adg): clean this up further; this is a bunch of hacked up code from cmd/upspin.
type Sharer struct {
	cfg upspin.Config
	cli upspin.Client
	dir upspin.DirServer
	key upspin.KeyServer

	// accessFiles contains the parsed Access files, keyed by directory to which it applies.
	accessFiles map[upspin.PathName]*access.Access

	// users caches per-directory user lists computed from Access files.
	users map[upspin.PathName]userList

	// userKeys holds the keys we've looked up for each user.
	userKeys map[upspin.UserName]upspin.PublicKey

	// userByHash maps the SHA-256 hashes of each user's key to the user name.
	userByHash map[[sha256.Size]byte]upspin.UserName
}

func newSharer(cfg upspin.Config, dir upspin.DirServer, key upspin.KeyServer) *Sharer {
	return &Sharer{
		cfg: cfg,
		cli: client.New(cfg),
		dir: dir,
		key: key,

		accessFiles: make(map[upspin.PathName]*access.Access),
		users:       make(map[upspin.PathName]userList),
		userKeys:    make(map[upspin.UserName]upspin.PublicKey),
		userByHash:  make(map[[sha256.Size]byte]upspin.UserName),
	}
}

// readers returns two lists, the list of users with access according to the
// access file, and the the pretty-printed string of user names recovered from
// looking at the list of hashed keys in the packdata.
// It also returns a boolean reporting whether key rewrapping is needed for self.
func (s *Sharer) readers(entry *upspin.DirEntry) (users, keyUsers userList, self bool, err error) {
	if entry.IsDir() {
		// Directories don't have readers.
		return nil, nil, self, nil
	}
	p, _ := path.Parse(entry.Name)
	for {
		p = p.Drop(1)
		var ok bool
		users, ok = s.users[p.Path()]
		if ok {
			break
		}
		if p.IsRoot() {
			users = userList{p.User()}
			break
		}
	}
	for _, user := range users {
		if _, err := s.lookupKey(user); err != nil {
			log.Error.Printf("watcher: %v: %v", entry.Name, err)
		}
	}
	packer := s.lookupPacker(entry)
	if packer == nil {
		return users, nil, self, errors.Errorf("no packer registered for packer %s", entry.Packing)
	}
	hashes, err := packer.ReaderHashes(entry.Packdata)
	if err != nil {
		return nil, nil, self, err
	}
	for _, hash := range hashes {
		var thisUser upspin.UserName
		switch packer.Packing() {
		case upspin.EEPack:
			if len(hash) != sha256.Size {
				log.Error.Printf("watcher: %v: hash size is %d; expected %d", entry.Name, len(hash), sha256.Size)
				continue
			}
			var h [sha256.Size]byte
			copy(h[:], hash)
			var ok bool
			thisUser, ok = s.userByHash[h]
			if !ok {
				// Check old keys in Factotum.
				f := s.cfg.Factotum()
				if _, err := f.PublicKeyFromHash(hash); err == nil {
					thisUser = s.cfg.UserName()
					ok = true
					self = true
				}
			}
			if !ok && bytes.Equal(factotum.AllUsersKeyHash, hash) {
				ok = true
				thisUser = access.AllUsers
			}
			if !ok {
				thisUser = "unknown"
			}
		default:
			log.Error.Printf("watcher: %v: unrecognized packing %s", entry.Name, packer)
			continue
		}
		keyUsers = append(keyUsers, thisUser)
	}
	return users, keyUsers, self, nil
}

// lookupPacker returns the Packer implementation for the entry, or
// nil if none is available.
func (s *Sharer) lookupPacker(entry *upspin.DirEntry) upspin.Packer {
	if entry.IsDir() {
		// Directories are not packed.
		return nil
	}
	packer := pack.Lookup(entry.Packing)
	if packer == nil {
		log.Error.Printf("watcher: %v: no registered packer for %d; ignoring\n", entry.Name, entry.Packing)
	}
	return packer
}

// addAccess reads the given Access file, adds it to the accessFiles map, and
// adds the readers defined by the Access file to the users map.
func (s *Sharer) addAccess(name upspin.PathName) error {
	b, err := s.cli.Get(name)
	if err != nil {
		return err
	}
	a, err := access.Parse(name, b)
	if err != nil {
		return err
	}
	readers, err := a.Users(access.Read, s.cli.Get)
	if err != nil {
		return errors.E(name, err)
	}
	dir := path.DropPath(name, 1)
	s.accessFiles[dir] = a
	s.users[dir] = userList(readers)
	return nil
}

// removeAccess removes the given Access file and its readers set from the
// accessFiles and users maps.
func (s *Sharer) removeAccess(name upspin.PathName) {
	dir := path.DropPath(name, 1)
	delete(s.accessFiles, dir)
	delete(s.users, dir)
}

// fixShare updates the packdata of the named file to contain wrapped keys for all the users.
func (s *Sharer) fixShare(entry *upspin.DirEntry, users userList) error {
	if entry.IsDir() {
		return errors.E(entry.Name, errors.IsDir, "cannot fix directory")
	}
	packer := s.lookupPacker(entry) // Won't be nil.
	if packer.Packing() != upspin.EEPack {
		return errors.E(entry.Name, errors.Invalid, errors.Errorf("unexpected packing %v", packer))
	}
	// If it's an Access or Group file, share with all users.
	all := access.IsAccessControlFile(entry.Name)
	keys := make([]upspin.PublicKey, 0, len(users))
	for _, user := range users {
		if user == access.AllUsers {
			all = true
			continue
		}
		// Erroneous or wildcard users will have empty keys here, and be ignored.
		k, err := s.lookupKey(user)
		if err != nil {
			return errors.E(entry.Name, user, err)
		}
		if len(k) > 0 {
			keys = append(keys, k)
		}
	}
	// Add the AllUsersKey if we're sharing with all users.
	if all {
		keys = append(keys, upspin.AllUsersKey)
	}
	packer.Share(s.cfg, keys, []*[]byte{&entry.Packdata})
	if entry.Packdata == nil {
		return errors.E(entry.Name, "packing skipped")
	}
	_, err := s.dir.Put(entry)
	return err
}

// lookupKey returns the public key for the user.
// If the user does not exist, is the "all" user, or is a wildcard
// (*@example.com), it returns the empty string.
func (s *Sharer) lookupKey(user upspin.UserName) (upspin.PublicKey, error) {
	if user == access.AllUsers {
		return upspin.AllUsersKey, nil
	}
	key, ok := s.userKeys[user] // Use an empty (zero-valued) key to cache failed lookups.
	if ok {
		return key, nil
	}
	if user == access.AllUsers {
		s.userKeys[user] = "<all>"
		return "", nil
	}
	if isWildcardUser(user) {
		s.userKeys[user] = ""
		return "", nil
	}
	u, err := s.key.Lookup(user)
	if err != nil {
		s.userKeys[user] = ""
		return "", err
	}
	// Remember the lookup, failed or otherwise.
	key = u.PublicKey
	if len(key) == 0 {
		s.userKeys[user] = ""
		return "", errors.E(user, "empty public key")
	}

	s.userKeys[user] = key
	s.userByHash[sha256.Sum256([]byte(key))] = user
	return key, nil
}

func isWildcardUser(user upspin.UserName) bool {
	return strings.HasPrefix(string(user), "*@")
}

// userList stores a list of users, and its string representation
// presents them in sorted order for easy comparison.
type userList []upspin.UserName

func (u userList) Len() int           { return len(u) }
func (u userList) Less(i, j int) bool { return u[i] < u[j] }
func (u userList) Swap(i, j int)      { u[i], u[j] = u[j], u[i] }

// String returns a canonically formatted, sorted list of the users.
func (u userList) String() string {
	if u == nil {
		return "<nil>"
	}
	sort.Sort(u)
	userString := fmt.Sprint([]upspin.UserName(u))
	return userString[1 : len(userString)-1]
}
