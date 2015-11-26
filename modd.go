package modd

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cortesi/termlog"
	"github.com/rjeczalik/notify"
)

// Version is the modd release version
const Version = "0.1"

// Logger receives events as "debug", and is silenced by default
var Logger = defaultLogger()

func defaultLogger() termlog.Logger {
	l := termlog.NewLog()
	l.Quiet()
	return l
}

// isUnder takes two absolute paths, and returns true if child is under parent.
func isUnder(parent string, child string) bool {
	parent = filepath.ToSlash(parent)
	child = filepath.ToSlash(child)
	off := strings.Index(child, parent)
	if off == 0 && (len(child) == len(parent) || child[len(parent)] == '/') {
		return true
	}
	return false
}

// Notify events have absolute paths. We want to normalize these so that they
// are relative to the base path.
func normPath(bases []string, abspath string) (string, error) {
	for _, base := range bases {
		absbase, err := filepath.Abs(base)
		if isUnder(absbase, abspath) {
			if err != nil {
				return "", err
			}
			relpath, err := filepath.Rel(absbase, abspath)
			if err != nil {
				return "", err
			}
			return filepath.Join(base, relpath), nil
		}
	}
	return abspath, nil
}

func normPaths(bases []string, abspaths []string) ([]string, error) {
	ret := make([]string, len(abspaths))
	for i, p := range abspaths {
		norm, err := normPath(bases, p)
		if err != nil {
			return nil, err
		}
		ret[i] = norm
	}
	return ret, nil
}

// An existenceChecker checks the existence of a file
type existenceChecker interface {
	Check(p string) bool
}

type statExistenceChecker struct{}

func (sc statExistenceChecker) Check(p string) bool {
	_, err := os.Stat(p)
	if err == nil {
		return true
	}
	return false
}

// Mod encapsulates a set of changes
type Mod struct {
	Changed []string
	Deleted []string
	Added   []string
}

// All returns a single list of all changed files
func (mod Mod) All() []string {
	all := make(map[string]bool)
	for _, p := range mod.Changed {
		all[p] = true
	}
	for _, p := range mod.Added {
		all[p] = true
	}
	for _, p := range mod.Deleted {
		all[p] = true
	}
	return _keys(all)
}

// Empty checks if this mod set is empty
func (mod Mod) Empty() bool {
	if len(mod.Changed) > 0 || len(mod.Deleted) > 0 || len(mod.Added) > 0 {
		return false
	}
	return true
}

// Filter applies a filter, modifying the Mod struct in-place
func (mod *Mod) Filter(excludes []string) (*Mod, error) {
	changed, err := filterFiles(mod.Changed, excludes)
	if err != nil {
		return nil, err
	}
	deleted, err := filterFiles(mod.Deleted, excludes)
	if err != nil {
		return nil, err
	}
	added, err := filterFiles(mod.Added, excludes)
	if err != nil {
		return nil, err
	}
	return &Mod{Changed: changed, Deleted: deleted, Added: added}, nil
}

func (mod *Mod) normPaths(bases []string) (*Mod, error) {
	changed, err := normPaths(bases, mod.Changed)
	if err != nil {
		return nil, err
	}
	deleted, err := normPaths(bases, mod.Deleted)
	if err != nil {
		return nil, err
	}
	added, err := normPaths(bases, mod.Added)
	if err != nil {
		return nil, err
	}
	return &Mod{Changed: changed, Deleted: deleted, Added: added}, nil
}

func _keys(m map[string]bool) []string {
	if len(m) > 0 {
		keys := make([]string, len(m))
		i := 0
		for k := range m {
			keys[i] = k
			i++
		}
		sort.Strings(keys)
		return keys
	}
	return nil
}

// This function batches events up, and emits just a list of paths for files
// considered changed. It applies some heuristics to deal with short-lived
// temporary files and unreliable filesystem events. There are all sorts of
// challenges here, that mean we can only do a mediocre job as it stands.
//
// - There's no cross-platform way to get the source as well as the destination
// for Rename events.
// - Events can arrive out of order - i.e. we can get a removal event first
// then a creation event for a transient file.
// - Events seem to be unreliable on some platforms - i.e. we might get a
// removal event but never see a creation event.
// - Events appear nonsensical on some platforms - i.e. we sometimes get a
// Create event as well as a Remove event when a pre-existing file is removed.
//
// In the face of all this, all we can do is layer on a set of heuristics to
// try to get intuitive results.
func batch(batchTime time.Duration, exists existenceChecker, ch chan notify.EventInfo) *Mod {
	added := make(map[string]bool)
	removed := make(map[string]bool)
	changed := make(map[string]bool)
	renamed := make(map[string]bool)
	for {
		select {
		case evt := <-ch:
			Logger.SayAs("debug", "%s", evt)
			switch evt.Event() {
			case notify.Create:
				added[evt.Path()] = true
			case notify.Remove:
				removed[evt.Path()] = true
			case notify.Write:
				changed[evt.Path()] = true
			case notify.Rename:
				renamed[evt.Path()] = true
			}
		case <-time.After(batchTime):
			ret := &Mod{}
			for k := range renamed {
				// If a file is moved from A to B, we'll get separate rename
				// events for both A and B. The only way to know if it was the
				// source or destination is to check if the file exists.
				if exists.Check(k) {
					added[k] = true
				} else {
					removed[k] = true
				}
			}
			for k := range added {
				if exists.Check(k) {
					// If a file exists, and has been both added and
					// changed, we just mark it as added
					delete(changed, k)
					delete(removed, k)
				} else {
					// If a file has been added, and now does not exist, we
					// strike it everywhere. This probably means the file is
					// transient - i.e. has been quickly added and removed, or
					// we've just not recieved a removal notification.
					delete(added, k)
					delete(removed, k)
					delete(changed, k)
				}
			}
			for k := range removed {
				if exists.Check(k) {
					delete(removed, k)
				} else {
					delete(added, k)
					delete(changed, k)
				}
			}
			ret.Added = _keys(added)
			ret.Changed = _keys(changed)
			ret.Deleted = _keys(removed)
			return ret
		}
	}
}

// Watch watches a path p, batching events with duration batchTime. A list of
// strings are written to chan, representing all files changed, added or
// removed. We apply heuristics to cope with things like transient files and
// unreliable event notifications.
func Watch(paths []string, excludes []string, batchTime time.Duration, ch chan Mod) error {
	evtch := make(chan notify.EventInfo, 1024)
	for _, p := range paths {
		stat, err := os.Stat(p)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			p = path.Join(p, "...")
		}
		err = notify.Watch(p, evtch, notify.All)
		if err != nil {
			return err
		}
	}
	go func() {
		for {
			ret := batch(batchTime, statExistenceChecker{}, evtch)
			if ret != nil {
				ret, err := ret.normPaths(paths)
				if err != nil {
					Logger.Shout("Error normalising paths: %s", err)
				}
				ret, err = ret.Filter(excludes)
				if err != nil {
					Logger.Shout("Error filtering paths: %s", err)
				}
				if !ret.Empty() {
					ch <- *ret
				}
			}
		}
	}()
	return nil
}
