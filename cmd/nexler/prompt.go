package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// stdin is the shared reader every prompt* helper reads a line from.
var stdin = bufio.NewReader(os.Stdin)

// readLine reads one line from stdin, trimmed. ok is false if stdin is
// closed or exhausted (e.g. not an interactive terminal, or piped input
// ran out) — callers use this to stop instead of looping forever
// re-reading an EOF that will never produce input.
func readLine() (line string, ok bool) {
	s, err := stdin.ReadString('\n')
	s = strings.TrimSpace(s)
	if err != nil && s == "" {
		return "", false
	}
	return s, true
}

// prompt asks label, showing def as the value Enter accepts, and returns
// whatever the user typed (or def, on blank input). Falls back to def if
// stdin is exhausted too — safe for non-interactive invocations (CI,
// piped input) that simply didn't pass the corresponding flag.
func prompt(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, ok := readLine()
	if !ok || line == "" {
		return def
	}
	return line
}

// promptRequired is like prompt but has no default: it re-asks on blank
// input, and if stdin runs out before a non-empty answer is given (e.g.
// this is running non-interactively without the corresponding flag), it
// reports a clear error and exits instead of looping forever.
func promptRequired(label string) string {
	for {
		fmt.Printf("%s: ", label)
		line, ok := readLine()
		if !ok {
			fmt.Fprintf(os.Stderr, "\nnexler: %s is required (no input available — pass it as a flag for non-interactive use)\n", label)
			os.Exit(1)
		}
		if line != "" {
			return line
		}
	}
}

// promptBool asks a yes/no question, showing def as the value Enter
// accepts.
func promptBool(label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Printf("%s [%s]: ", label, hint)
		line, ok := readLine()
		if !ok || line == "" {
			return def
		}
		switch strings.ToLower(line) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Println(`please answer "y" or "n"`)
	}
}

// promptChoice asks label, listing options and showing def as the value
// Enter accepts, and returns whichever option (case-insensitively)
// matched — reprompting on anything else. def must be one of options.
func promptChoice(label string, options []string, def string) string {
	for {
		fmt.Printf("%s [%s] (%s): ", label, strings.Join(options, "/"), def)
		line, ok := readLine()
		if !ok || line == "" {
			return def
		}
		for _, opt := range options {
			if strings.EqualFold(line, opt) {
				return opt
			}
		}
		fmt.Printf("please answer one of: %s\n", strings.Join(options, ", "))
	}
}

// promptUIOrAPI asks whether to use the ui or api response style, showing
// def as the value Enter accepts, and returns true for "ui".
func promptUIOrAPI(label string, def bool) bool {
	choice := "api"
	if def {
		choice = "ui"
	}
	for {
		fmt.Printf("%s [api/ui] (%s): ", label, choice)
		line, ok := readLine()
		if !ok || line == "" {
			return def
		}
		switch strings.ToLower(line) {
		case "ui":
			return true
		case "api":
			return false
		}
		fmt.Println(`please answer "ui" or "api"`)
	}
}
