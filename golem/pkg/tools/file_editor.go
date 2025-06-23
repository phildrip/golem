package tools

import (
	"io/ioutil"
	"regexp"
)

// ReadFile reads the content of a file at the given path.
func ReadFile(path string) (string, error) {
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// WriteFile writes the given content to a file at the given path.
// If the file does not exist, it will be created. If it exists, it will be truncated.
func WriteFile(path string, content string) error {
	return ioutil.WriteFile(path, []byte(content), 0644)
}

// ReplaceStringWithRegex reads a file, replaces all occurrences of a regex pattern,
// and writes the content back to the file.
func ReplaceStringWithRegex(path string, pattern string, replacement string) error {
	// Read the file content
	content, err := ReadFile(path)
	if err != nil {
		return err
	}

	// Compile the regex pattern
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	// Replace all occurrences of the pattern
	newContent := re.ReplaceAllString(content, replacement)

	// Write the new content back to the file
	return WriteFile(path, newContent)
}
