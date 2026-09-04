package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errNotOMPSession = errors.New("not an OMP session file")

type OMPSession struct {
	ID           string
	Title        string
	CWD          string
	Path         string
	LastModified int64
}

type OMPFile struct {
	Header       map[string]any
	Entries      []any
	LastModified int64
}

func ListOMP(root string) ([]OMPSession, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return []OMPSession{}, nil
	}
	var sessions []OMPSession
	var readErrors []error
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			readErrors = append(readErrors, walkErr)
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		file, err := readOMP(path, false)
		if errors.Is(err, errNotOMPSession) {
			return nil
		}
		if err != nil {
			readErrors = append(readErrors, fmt.Errorf("read OMP session header %s: %w", path, err))
			return nil
		}
		sessions = append(sessions, OMPSession{
			ID:           stringValue(file.Header["id"]),
			Title:        stringValue(file.Header["title"]),
			CWD:          stringValue(file.Header["cwd"]),
			Path:         path,
			LastModified: file.LastModified,
		})
		return nil
	})
	if err != nil {
		readErrors = append(readErrors, err)
	}
	return sessions, errors.Join(readErrors...)
}

func ReadOMP(path string) (OMPFile, error) {
	file, err := readOMP(path, true)
	if errors.Is(err, errNotOMPSession) {
		return OMPFile{}, fmt.Errorf("%s is not an OMP session file", path)
	}
	return file, err
}

func readOMP(path string, includeEntries bool) (OMPFile, error) {
	input, err := os.Open(path)
	if err != nil {
		return OMPFile{}, err
	}
	defer func() { _ = input.Close() }()

	info, err := input.Stat()
	if err != nil {
		return OMPFile{}, err
	}
	result := OMPFile{Entries: []any{}, LastModified: info.ModTime().UnixMilli()}
	var title map[string]any
	reader := bufio.NewReader(input)
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var entry map[string]any
			if decodeErr := json.Unmarshal(trimmed, &entry); decodeErr != nil {
				if readErr == io.EOF {
					break
				}
				continue
			}
			switch stringValue(entry["type"]) {
			case "title":
				if result.Header == nil {
					title = entry
					break
				}
				result.Entries = append(result.Entries, entry)
			case "session":
				if result.Header != nil {
					result.Entries = append(result.Entries, entry)
					break
				}
				if !ValidOMPID(stringValue(entry["id"])) {
					return OMPFile{}, errNotOMPSession
				}
				result.Header = entry
				if title != nil {
					if current := stringValue(title["title"]); current != "" {
						result.Header["title"] = current
					}
					if source := stringValue(title["source"]); source != "" {
						result.Header["titleSource"] = source
					}
				}
				if !includeEntries {
					return result, nil
				}
			default:
				if result.Header == nil {
					return OMPFile{}, errNotOMPSession
				}
				result.Entries = append(result.Entries, entry)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return OMPFile{}, readErr
		}
	}
	if result.Header == nil {
		return OMPFile{}, errNotOMPSession
	}
	return result, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
