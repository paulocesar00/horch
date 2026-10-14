package main

import (
	"encoding/json"
	"fmt"
	"github.com/ellingtonsp/herdr-orch/internal/config"
)

func cmdConfig(args []string) error {
	f := fs("config")
	if _, err := parse(f, args); err != nil {
		return err
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	missing := c.Missing()
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"config": c, "missing": missing})
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("config: %s (found=%v)\n", c.File, c.Found)
	fmt.Printf("owner: %s  default project: %s\n", or(c.Owner.Principal, "-"), or(c.DefaultProject, "-"))
	for _, m := range missing {
		fmt.Println("missing:", m)
	}
	return nil
}
