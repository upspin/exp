package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/ioutil"
	"sort"
	"strings"
	"sync"

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
	"upspin.io/upspin"

	_ "upspin.io/transports"
)

func main() {
	flags.Parse(flags.Client)

	cfg, err := config.FromFile(flags.Config)
	if err != nil {
		log.Fatal(err)
	}

	dir, err := bind.DirServer(cfg, cfg.DirEndpoint())
	if err != nil {
		log.Fatal(err)
	}

	var (
		name       = upspin.PathName(cfg.UserName() + "/")
		seq  int64 = -1
		done       = make(chan struct{})
	)
	events, err := dir.Watch(name, seq, done)
	if err != nil {
		log.Fatal(err)
	}

	var (
		filesMu   sync.Mutex
		filesCond = sync.NewCond(&filesMu) // Signalled on new files.
		files     = make(map[upspin.PathName]*upspin.DirEntry)

		sMu sync.Mutex
		s   = newSharer(cfg)
	)

	checkEntry := func(e *upspin.DirEntry) {
		filesMu.Lock()
		oldE, ok := files[e.Name]
		if !ok || oldE.Sequence < e.Sequence {
			files[e.Name] = e
		}
		filesMu.Unlock()
		if !ok {
			filesCond.Signal()
		}
	}

	var addPath func(upspin.PathName)
	addPath = func(name upspin.PathName) {
		des, err := dir.Glob(upspin.AllFilesGlob(name))
		if err != nil {
			log.Print(err)
		}
		for _, e := range des {
			if access.IsAccessControlFile(e.Name) {
				continue
			}
			if e.IsDir() {
				// If there's no Access file in the
				// directory then descend into it.
				accessFile := path.Join(e.Name, "Access")
				_, err := dir.Lookup(accessFile)
				if errors.Is(errors.NotExist, err) {
					addPath(e.Name)
				}
				continue
			}
			checkEntry(e)
		}
	}

	go func() {
		for {
			// Grab a new file from the map, or wait for a file to
			// be put in there if there are none.
			filesMu.Lock()
			for len(files) == 0 {
				filesCond.Wait()
			}
			var e *upspin.DirEntry
			for _, e = range files {
				delete(files, e.Name)
				break
			}
			filesMu.Unlock()

			if e.Packing != upspin.EEPack {
				continue
			}
			sMu.Lock()
			readers, keyUsers, self, err := s.readers(e)
			sMu.Unlock()
			if err != nil {
				log.Print(err)
				continue
			}
			msg := fmt.Sprintf("%v self=%v\n\treaders: %v\n\tkeyUsers: %v", e.Name, self, readers, keyUsers)
			if !self && readers.String() == keyUsers.String() {
				log.Debug.Print(msg)
				continue
			}
			log.Error.Printf("fixing inconsistency: %v", msg)
			sMu.Lock()
			s.fixShare(e.Name, readers)
			sMu.Unlock()
		}
	}()

	for {
		log.Debug.Print("waiting for event")
		e, ok := <-events
		if !ok {
			break
		}
		log.Debug.Printf("received event: %v delete=%t seq=%d", e.Entry.Name, e.Delete, e.Entry.Sequence)
		if e.Entry.IsDir() {
			continue
		}
		if access.IsAccessControlFile(e.Entry.Name) {
			sMu.Lock()
			if e.Delete {
				log.Debug.Printf("removeAccess: %v", e.Entry.Name)
				s.removeAccess(e.Entry)
			} else {
				log.Debug.Printf("addAccess: %v", e.Entry.Name)
				s.addAccess(e.Entry)
			}
			sMu.Unlock()

			p, _ := path.Parse(e.Entry.Name)
			go addPath(p.Drop(1).Path())
			continue
		}
		if !e.Delete {
			checkEntry(e.Entry)
		}
	}
}

// Sharer holds the state for the share calculation. It holds some caches to
// avoid calling on the server too much.
type Sharer struct {
	state struct {
		upspin.Client
		upspin.Config
		log.Logger
		ExitCode int
	}
	// Flags.
	fix bool

	// accessFiles contains the parsed Access files, keyed by directory to which it applies.
	accessFiles map[upspin.PathName]*access.Access

	// users caches per-directory user lists computed from Access files.
	users map[upspin.PathName]userList

	// userKeys holds the keys we've looked up for each user.
	userKeys map[upspin.UserName]upspin.PublicKey

	// userByHash maps the SHA-256 hashes of each user's key to the user name.
	userByHash map[[sha256.Size]byte]upspin.UserName
}

func newSharer(cfg upspin.Config) *Sharer {
	s := &Sharer{
		accessFiles: make(map[upspin.PathName]*access.Access),
		users:       make(map[upspin.PathName]userList),
		userKeys:    make(map[upspin.UserName]upspin.PublicKey),
		userByHash:  make(map[[sha256.Size]byte]upspin.UserName),
	}
	s.state.Client = client.New(cfg)
	s.state.Config = cfg
	s.state.Logger = log.Error
	return s
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
		s.lookupKey(user)
	}
	packer := s.lookupPacker(entry)
	if packer == nil {
		return users, nil, self, errors.Errorf("no packer registered for packer %s", entry.Packing)
	}
	if packer.Packing() != upspin.EEPack { // TODO: add new sharing packers here.
		return users, nil, self, nil
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
				s.state.Printf("%q hash size is %d; expected %d", entry.Name, len(hash), sha256.Size)
				s.state.ExitCode = 1
				continue
			}
			var h [sha256.Size]byte
			copy(h[:], hash)
			var ok bool
			thisUser, ok = s.userByHash[h]
			if !ok {
				// Check old keys in Factotum.
				f := s.state.Config.Factotum()
				if f == nil {
					s.state.Fatalf("no factotum available")
				}
				if _, err := f.PublicKeyFromHash(hash); err == nil {
					thisUser = s.state.Config.UserName()
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
			s.state.Printf("%q: unrecognized packing %s", entry.Name, packer)
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
		s.state.Printf("%q has no registered packer for %d; ignoring\n", entry.Name, entry.Packing)
	}
	return packer
}

// addAccess loads an access file.
func (s *Sharer) addAccess(entry *upspin.DirEntry) {
	name := entry.Name
	if !entry.IsDir() {
		name = path.DropPath(name, 1) // Directory name for this file.
	}
	if _, ok := s.accessFiles[name]; ok {
		return
	}
	which, err := s.DirServer(name).WhichAccess(entry.Name) // Guaranteed to have no links.
	if err != nil {
		s.state.Printf("looking up access file %q: %s", name, err)
		return
	}
	var a *access.Access
	if which == nil {
		a, err = access.New(name)
	} else {
		b, err := read(s.state.Client, which.Name)
		if err != nil {
			s.state.Printf("reading access file: %v", err)
			return
		}
		a, err = access.Parse(which.Name, b)
	}
	if err != nil {
		s.state.Printf("parsing access file %q: %s", name, err)
		return
	}
	s.accessFiles[name] = a
	s.users[name] = s.usersWithAccess(s.state.Client, a, access.Read)
}

func (s *Sharer) removeAccess(entry *upspin.DirEntry) {
	name := entry.Name
	if !entry.IsDir() {
		name = path.DropPath(name, 1) // Directory name for this file.
	}
	delete(s.accessFiles, name)
	delete(s.users, name)
}

// usersWithReadAccess returns the list of user names granted access by this access file.
func (s *Sharer) usersWithAccess(client upspin.Client, a *access.Access, right access.Right) userList {
	if a == nil {
		return nil
	}
	users, err := a.Users(right, client.Get)
	if err != nil {
		s.state.Printf("getting user list: %s", err)
	}
	return userList(users)
}

// read returns the contents of the file.
func read(c upspin.Client, file upspin.PathName) ([]byte, error) {
	fd, err := c.Open(file)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	data, err := ioutil.ReadAll(fd)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// fixShare updates the packdata of the named file to contain wrapped keys for all the users.
func (s *Sharer) fixShare(name upspin.PathName, users userList) {
	directory := s.DirServer(name)
	entry, err := directory.Lookup(name) // Guaranteed to have no links.
	if err != nil {
		s.state.Printf("looking up %q: %s", name, err)
		s.state.ExitCode = 1
		return
	}
	if entry.IsDir() {
		s.state.Fatalf("internal error: fixShare called on directory %q", name)
	}
	packer := s.lookupPacker(entry) // Won't be nil.
	switch packer.Packing() {
	case upspin.EEPack:
		// Will repack below.
	default:
		return
	}
	// Could do this more efficiently, calling Share collectively, but the Puts are sequential anyway.
	keys := make([]upspin.PublicKey, 0, len(users))
	all := access.IsAccessControlFile(entry.Name)
	for _, user := range users {
		if user == access.AllUsers {
			all = true
			continue
		}
		// Erroneous or wildcard users will have empty keys here, and be ignored.
		if k := s.lookupKey(user); len(k) > 0 {
			// TODO: Make this general. This works now only because we are always using ee.
			keys = append(keys, k)
			continue
		}
		s.state.Printf("%q: user %q has no key for packing %s\n", entry.Name, user, packer)
		s.state.ExitCode = 1
		return
	}
	if all {
		keys = append(keys, upspin.AllUsersKey)
	}
	packer.Share(s.state.Config, keys, []*[]byte{&entry.Packdata})
	if entry.Packdata == nil {
		s.state.Printf("packing skipped for %q\n", entry.Name)
		s.state.ExitCode = 1
		return
	}
	_, err = directory.Put(entry)
	if err != nil {
		s.state.Printf("error putting entry back for %q: %s\n", name, err)
		s.state.ExitCode = 1
	}
}

// lookupKey returns the public key for the user.
// If the user does not exist, is the "all" user, or is a wildcard
// (*@example.com), it returns the empty string.
func (s *Sharer) lookupKey(user upspin.UserName) upspin.PublicKey {
	if user == access.AllUsers {
		return upspin.AllUsersKey
	}
	key, ok := s.userKeys[user] // We use an empty (zero-valued) key to cache failed lookups.
	if ok {
		return key
	}
	if user == access.AllUsers {
		s.userKeys[user] = "<all>"
		return ""
	}
	if isWildcardUser(user) {
		s.userKeys[user] = ""
		return ""
	}
	u, err := s.KeyServer().Lookup(user)
	if err != nil {
		s.state.Printf("can't find key for %q: %s\n", user, err)
		s.state.ExitCode = 1
		s.userKeys[user] = ""
		return ""
	}
	// Remember the lookup, failed or otherwise.
	key = u.PublicKey
	if len(key) == 0 {
		s.state.Printf("no key for %q\n", user)
		s.state.ExitCode = 1
		s.userKeys[user] = ""
		return ""
	}

	s.userKeys[user] = key
	s.userByHash[sha256.Sum256([]byte(key))] = user
	return key
}

func isWildcardUser(user upspin.UserName) bool {
	return strings.HasPrefix(string(user), "*@")
}

func (s *Sharer) KeyServer() upspin.KeyServer {
	key, err := bind.KeyServer(s.state.Config, s.state.Config.KeyEndpoint())
	if err != nil {
		log.Fatal(err)
	}
	return key
}

func (s *Sharer) DirServer(name upspin.PathName) upspin.DirServer {
	p, _ := path.Parse(name)
	dir, err := bind.DirServerFor(s.state.Config, p.User())
	if err != nil {
		log.Fatal(err)
	}
	return dir
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
