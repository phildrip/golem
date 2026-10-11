package importer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/memory/fractal"
)

// HandleImportOption processes the -import CLI argument.
// Returns true if an import was performed.
func HandleImportOption(importArg string, store *fractal.Store, workspaceDir string, in io.Reader, out io.Writer) (bool, error) {
	if importArg == "" {
		return false, nil
	}

	cleanArg := strings.TrimSpace(importArg)
	sessions, err := DiscoverHistories(workspaceDir)
	if err != nil {
		return false, errors.Wrap(err, "discovering local agent histories")
	}

	var targetSession *DiscoveredSession

	switch cleanArg {
	case "pick", "true", "interactive", "?":
		targetSession, err = promptUserSessionPicker(sessions, in, out)
		if err != nil {
			return false, err
		}
		if targetSession == nil {
			fmt.Fprintln(out, "Import cancelled.")
			return false, nil
		}

	case "latest":
		if len(sessions) == 0 {
			return false, errors.New("no importable agent histories found")
		}
		targetSession = &sessions[0]

	default:
		// Check if argument is an existing path
		if _, statErr := os.Stat(cleanArg); statErr == nil {
			count, impErr := ImportFileOrDir(cleanArg, store)
			if impErr != nil {
				return false, errors.Wrapf(impErr, "importing from path %q", cleanArg)
			}
			fmt.Fprintf(out, "✓ Imported %d events from %q into fractal memory.\n", count, cleanArg)
			return true, nil
		}

		// Otherwise match by session ID
		for i := range sessions {
			if strings.EqualFold(sessions[i].ID, cleanArg) || strings.HasPrefix(sessions[i].ID, cleanArg) {
				targetSession = &sessions[i]
				break
			}
		}
		if targetSession == nil {
			return false, errors.Errorf("no discovered session matching ID or path %q", cleanArg)
		}
	}

	if targetSession != nil {
		count, impErr := ImportSession(*targetSession, store)
		if impErr != nil {
			return false, errors.Wrapf(impErr, "importing session %s", targetSession.ID)
		}
		fmt.Fprintf(out, "✓ Imported %d events from [%s] session %q into fractal memory.\n",
			count, strings.ToUpper(string(targetSession.Format)), targetSession.Title)
		return true, nil
	}

	return false, nil
}

func promptUserSessionPicker(sessions []DiscoveredSession, in io.Reader, out io.Writer) (*DiscoveredSession, error) {
	if len(sessions) == 0 {
		return nil, errors.New("no importable histories found in local environment (checked Antigravity, Cursor & OpenCode)")
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "================================================================================")
	fmt.Fprintln(out, "                         Golem History Import Picker")
	fmt.Fprintln(out, "================================================================================")
	fmt.Fprintf(out, "Found %d session(s) in local environment:\n\n", len(sessions))

	// Display table header
	fmt.Fprintf(out, "%-4s %-8s %-7s %-19s  %s\n", "#", "SOURCE", "MATCH", "UPDATED (UTC)", "TITLE / SUMMARY")
	fmt.Fprintln(out, strings.Repeat("-", 80))

	limit := len(sessions)
	if limit > 25 {
		limit = 25
	}

	for i := 0; i < limit; i++ {
		s := sessions[i]
		matchStr := " "
		if s.MatchesWorkspace {
			matchStr = "YES"
		}
		fmt.Fprintf(out, "[%2d] %-8s %-7s %-19s  %s\n",
			i+1,
			"["+strings.ToUpper(string(s.Format))+"]",
			matchStr,
			s.UpdatedAt.Format(time.DateTime),
			s.Title,
		)
	}

	if len(sessions) > limit {
		fmt.Fprintf(out, "\n... and %d older session(s) truncated\n", len(sessions)-limit)
	}
	fmt.Fprintln(out, strings.Repeat("-", 80))

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprintf(out, "\nEnter session number to import (1-%d, or 'q' to cancel): ", limit)
		if !scanner.Scan() {
			return nil, errors.New("input stream closed")
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "q" || text == "exit" || text == "quit" {
			return nil, nil
		}

		choice, err := strconv.Atoi(text)
		if err != nil || choice < 1 || choice > limit {
			fmt.Fprintf(out, "Invalid selection %q. Please enter a number between 1 and %d.\n", text, limit)
			continue
		}

		selected := sessions[choice-1]
		return &selected, nil
	}
}
