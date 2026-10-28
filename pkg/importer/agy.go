package importer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/memory/fractal"
)

var userRequestRegex = regexp.MustCompile(`(?s)<USER_REQUEST>(.*?)</USER_REQUEST>`)

// agyStep matches the JSON structure of a step in transcript.jsonl.
type agyStep struct {
	StepIndex int       `json:"step_index"`
	Source    string    `json:"source"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
	Content   string    `json:"content,omitempty"`
	Thinking  string    `json:"thinking,omitempty"`
	ToolCalls []agyTool `json:"tool_calls,omitempty"`
}

type agyTool struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ParseAGY parses an Antigravity transcript from an io.Reader into normalized ImportedEvents.
func ParseAGY(r io.Reader) ([]ImportedEvent, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var events []ImportedEvent

	for {
		lineBytes, readErr := reader.ReadBytes('\n')
		line := strings.TrimSpace(string(lineBytes))
		if line != "" {
			var step agyStep
			if err := json.Unmarshal([]byte(line), &step); err == nil {
				ts := parseTimestamp(step.CreatedAt)

				switch step.Type {
				case "USER_INPUT":
					text := cleanUserContent(step.Content)
					if text != "" {
						events = append(events, ImportedEvent{
							Kind:      fractal.KindUser,
							Text:      text,
							Timestamp: ts,
						})
					}

				case "PLANNER_RESPONSE":
					if strings.TrimSpace(step.Content) != "" {
						events = append(events, ImportedEvent{
							Kind:      fractal.KindTalk,
							Text:      strings.TrimSpace(step.Content),
							Timestamp: ts,
						})
					}
					for _, tc := range step.ToolCalls {
						argsStr := string(tc.Args)
						if argsStr == "" || argsStr == "null" {
							argsStr = "{}"
						}
						events = append(events, ImportedEvent{
							Kind:      fractal.KindTool,
							Text:      fmt.Sprintf("%s: %s", tc.Name, argsStr),
							Timestamp: ts,
						})
					}

				case "TOOL_RESPONSE", "TOOL_RESULT", "RUN_COMMAND", "VIEW_FILE", "GREP_SEARCH", "LIST_DIRECTORY", "SEARCH_WEB", "CODE_ACTION", "MCP_TOOL", "READ_URL_CONTENT", "GENERATE_IMAGE", "ERROR_MESSAGE":
					if strings.TrimSpace(step.Content) != "" {
						events = append(events, ImportedEvent{
							Kind:      fractal.KindEcho,
							Text:      strings.TrimSpace(step.Content),
							Timestamp: ts,
						})
					}

				case "INVOKE_SUBAGENT", "SUBAGENT_RESULT", "SUBAGENT_DONE":
					if strings.TrimSpace(step.Content) != "" {
						events = append(events, ImportedEvent{
							Kind:      fractal.KindWork,
							Text:      strings.TrimSpace(step.Content),
							Timestamp: ts,
						})
					}

				default:
					if step.Source == "USER_EXPLICIT" {
						text := cleanUserContent(step.Content)
						if text != "" {
							events = append(events, ImportedEvent{
								Kind:      fractal.KindUser,
								Text:      text,
								Timestamp: ts,
							})
						}
					} else if strings.TrimSpace(step.Content) != "" {
						events = append(events, ImportedEvent{
							Kind:      fractal.KindTalk,
							Text:      strings.TrimSpace(step.Content),
							Timestamp: ts,
						})
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return events, errors.Wrap(readErr, "reading AGY transcript")
		}
	}

	return events, nil
}

// ParseAGYPath parses an AGY file or directory.
func ParseAGYPath(path string) ([]ImportedEvent, error) {
	filePath := path
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errors.Wrapf(err, "stat AGY path %q", path)
	}

	if fi.IsDir() {
		candidate1 := filepath.Join(path, ".system_generated", "logs", "transcript.jsonl")
		candidate2 := filepath.Join(path, "transcript.jsonl")
		if fileExists(candidate1) {
			filePath = candidate1
		} else if fileExists(candidate2) {
			filePath = candidate2
		} else {
			return nil, errors.Errorf("no transcript.jsonl found in directory %q", path)
		}
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, errors.Wrapf(err, "opening AGY file %q", filePath)
	}
	defer f.Close()

	return ParseAGY(f)
}

func cleanUserContent(content string) string {
	matches := userRequestRegex.FindStringSubmatch(content)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return strings.TrimSpace(content)
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t.UTC()
	}
	t, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}
