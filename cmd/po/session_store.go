package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lemonzjj/po-agent-go/session"
	sessionjsonl "github.com/lemonzjj/po-agent-go/session/jsonl"
)

type openedSession struct {
	Session *session.Session
	Journal *sessionjsonl.File
	Path    string
}

func defaultSessionsDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "po", "sessions"), nil
}

func createSession() (*openedSession, error) {
	return createSessionWithOptions(session.Options{})
}

func createSessionWithOptions(options session.Options) (*openedSession, error) {
	dir, err := defaultSessionsDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("%d", now.UnixNano())
	path := filepath.Join(dir, id+".jsonl")
	journal, err := sessionjsonl.Create(path, id, now)
	if err != nil {
		return nil, err
	}
	sess, err := session.NewWithOptions(id, now, journal, options)
	if err != nil {
		journal.Close()
		return nil, err
	}
	return &openedSession{Session: sess, Journal: journal, Path: path}, nil
}

func openSession(path string) (*openedSession, error) {
	return openSessionWithOptions(path, session.Options{})
}

func openSessionWithOptions(path string, options session.Options) (*openedSession, error) {
	journal, state, err := sessionjsonl.Open(path)
	if err != nil {
		return nil, err
	}
	sess, err := session.ResumeWithOptions(state, journal, options)
	if err != nil {
		journal.Close()
		return nil, err
	}
	return &openedSession{Session: sess, Journal: journal, Path: path}, nil
}

func latestSessionPath() (string, error) {
	dir, err := defaultSessionsDir()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	type item struct {
		path string
		mod  time.Time
	}
	items := []item{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, entry.Name()), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	if len(items) == 0 {
		return "", nil
	}
	return items[0].path, nil
}

func (s *openedSession) Close() error {
	if s == nil || s.Journal == nil {
		return nil
	}
	return s.Journal.Close()
}
