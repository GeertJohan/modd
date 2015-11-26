package main

import (
	"fmt"
	"time"

	"github.com/cortesi/modd"
	"gopkg.in/alecthomas/kingpin.v2"
)

const batchTime = time.Millisecond * 200

func main() {
	paths := kingpin.Arg(
		"path",
		"Paths to monitor for changes.",
	).Required().Strings()
	kingpin.Parse()

	modchan := make(chan modd.Mod)
	for _, path := range *paths {
		err := modd.Watch(path, batchTime, modchan)
		if err != nil {
			kingpin.Fatalf("Fatal error: %s", err)
		}
	}
	for mod := range modchan {
		if len(mod.Added) > 0 {
			fmt.Printf("Added: %v\n", mod.Added)
		}
		if len(mod.Changed) > 0 {
			fmt.Printf("Changed: %v\n", mod.Changed)
		}
		if len(mod.Deleted) > 0 {
			fmt.Printf("Removed: %v\n", mod.Deleted)
		}
	}
}
