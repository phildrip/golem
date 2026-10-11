package importer

import (
	"github.com/pkg/errors"
	"github.com/user/golem/pkg/memory/fractal"
)

// ImportFileOrDir deduces the format of the path, parses historical events,
// and appends them to targetStore preserving their historical timestamps.
// An optional sessionID can be passed to target a specific composer/session within a store.
func ImportFileOrDir(path string, targetStore *fractal.Store, sessionID ...string) (int, error) {
	if targetStore == nil {
		return 0, errors.New("targetStore cannot be nil")
	}

	format, err := DeduceFormat(path)
	if err != nil {
		return 0, errors.Wrap(err, "deducing format")
	}

	targetID := ""
	if len(sessionID) > 0 {
		targetID = sessionID[0]
	}

	var events []ImportedEvent
	switch format {
	case FormatAGY:
		events, err = ParseAGYPath(path)
		if err != nil {
			return 0, errors.Wrap(err, "parsing AGY transcript")
		}
	case FormatCursor:
		events, err = ParseCursor(path, targetID)
		if err != nil {
			return 0, errors.Wrap(err, "parsing Cursor history")
		}
	case FormatOpenCode:
		events, err = ParseOpenCode(path, targetID)
		if err != nil {
			return 0, errors.Wrap(err, "parsing OpenCode history")
		}
	default:
		return 0, errors.Errorf("unsupported or unknown format for %q", path)
	}

	if len(events) == 0 {
		return 0, errors.New("no events parsed from source")
	}

	count := 0
	for _, ev := range events {
		opts := fractal.AppendEventOpts{
			Timestamp: ev.Timestamp,
		}
		_, err := targetStore.AppendEvent(ev.Kind, ev.Text, opts)
		if err != nil {
			return count, errors.Wrapf(err, "appending event #%d to store", count)
		}
		count++
	}

	return count, nil
}

// ImportSession imports a specific DiscoveredSession into the targetStore.
func ImportSession(session DiscoveredSession, targetStore *fractal.Store) (int, error) {
	return ImportFileOrDir(session.Path, targetStore, session.ID)
}
