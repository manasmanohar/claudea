package main

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

const usageText = `claudea — switch Claude Code between your saved accounts

  claudea              open the account switcher (usage for every account, Enter to switch)
  claudea ls           list saved accounts (✓ = the one Claude Code uses now)
  claudea use <name>   switch without opening the switcher
  claudea -h           show this help
`

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0:
		_, err = tea.NewProgram(newModel(), tea.WithAltScreen()).Run()
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		fmt.Print(usageText)
	case args[0] == "ls" && len(args) == 1:
		all := names()
		if len(all) == 0 {
			fmt.Println("no saved accounts yet — run claudea to add one")
			return
		}
		act := activeName()
		for i, n := range all {
			mark := ""
			if n == act {
				mark = " ✓"
			}
			fmt.Printf("%-3d %-14s %s%s\n", i+1, n, loadProfile(n).str("emailAddress"), mark)
		}
	case args[0] == "use" && len(args) == 2:
		if activeName() == args[1] {
			fmt.Printf("already on '%s'\n", args[1])
			return
		}
		if err = use(args[1]); err == nil {
			fmt.Printf("switched to '%s'\n", args[1])
		} else if errors.Is(err, errUnsaved) {
			err = fmt.Errorf("you're signed in as %s, which isn't saved yet — run claudea and press Enter on it to save it, then switch",
				liveProfile().str("emailAddress"))
		}
	default:
		fmt.Fprintf(os.Stderr, "claudea: unknown command '%s'\n\n%s", args[0], usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "claudea:", err)
		os.Exit(1)
	}
}
