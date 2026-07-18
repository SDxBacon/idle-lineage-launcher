package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	testUpdateFromCommit   = "1111111111111111111111111111111111111111"
	testUpdateTargetCommit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
)

func TestUpdateJournalRoundTrip(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	store := newUpdateJournalStore(paths)
	want := validTestUpdateJournal(paths.GameRoot)

	if err := store.Save(want); err != nil {
		t.Fatalf("save update journal: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load update journal: %v", err)
	}
	want.SchemaVersion = updateJournalSchemaVersion
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("loaded journal = %#v, want %#v", got, want)
	}
}

func TestUpdateJournalSaveAtomicallyReplacesExistingJournal(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	store := newUpdateJournalStore(paths)
	first := validTestUpdateJournal(paths.GameRoot)
	if err := store.Save(first); err != nil {
		t.Fatalf("save initial update journal: %v", err)
	}

	second := first
	second.Strategy = updateJournalStrategyReplace
	second.Phase = updateJournalPhaseCommitted
	second.TargetCommit = "2222222222222222222222222222222222222222"
	if err := store.Save(second); err != nil {
		t.Fatalf("replace update journal: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load replacement update journal: %v", err)
	}
	second.SchemaVersion = updateJournalSchemaVersion
	if got == nil || !reflect.DeepEqual(*got, second) {
		t.Fatalf("loaded replacement = %#v, want %#v", got, second)
	}
	temporary, err := filepath.Glob(filepath.Join(paths.Root, ".update-journal-*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary journals: %v", err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary journals left after replacement: %v", temporary)
	}
}

func TestUpdateJournalLoadMissingReturnsNil(t *testing.T) {
	paths := makeDataPaths(filepath.Join(t.TempDir(), "missing"))
	journal, err := newUpdateJournalStore(paths).Load()
	if err != nil {
		t.Fatalf("load missing update journal: %v", err)
	}
	if journal != nil {
		t.Fatalf("missing update journal = %#v, want nil", journal)
	}
	if _, err := os.Stat(paths.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load should not create journal directory; stat error = %v", err)
	}
}

func TestUpdateJournalLoadRejectsInvalidRecordsWithoutRemovingThem(t *testing.T) {
	root := t.TempDir()
	paths := makeDataPaths(root)
	valid := `{"schemaVersion":1,"gameRoot":` + quotedJSONPath(paths.GameRoot) + `,"fromCommit":"` + testUpdateFromCommit + `","targetCommit":"` + testUpdateTargetCommit + `","strategy":"in_place","phase":"prepared"}`
	tests := []struct {
		name     string
		contents string
	}{
		{name: "malformed JSON", contents: `{"schemaVersion":`},
		{name: "multiple values", contents: valid + `{}`},
		{name: "unknown field", contents: strings.TrimSuffix(valid, "}") + `,"unexpected":true}`},
		{name: "unsupported schema", contents: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{name: "missing game root", contents: strings.Replace(valid, `"gameRoot":`+quotedJSONPath(paths.GameRoot), `"gameRoot":""`, 1)},
		{name: "mismatched game root", contents: strings.Replace(valid, quotedJSONPath(paths.GameRoot), quotedJSONPath(filepath.Join(root, "other")), 1)},
		{name: "short source commit", contents: strings.Replace(valid, testUpdateFromCommit, "1234", 1)},
		{name: "non-hex target commit", contents: strings.Replace(valid, testUpdateTargetCommit, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", 1)},
		{name: "zero source commit", contents: strings.Replace(valid, testUpdateFromCommit, "0000000000000000000000000000000000000000", 1)},
		{name: "invalid strategy", contents: strings.Replace(valid, `"strategy":"in_place"`, `"strategy":"copy"`, 1)},
		{name: "invalid phase", contents: strings.Replace(valid, `"phase":"prepared"`, `"phase":"started"`, 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(paths.UpdateJournal, []byte(test.contents), 0o600); err != nil {
				t.Fatalf("write invalid update journal: %v", err)
			}
			if journal, err := newUpdateJournalStore(paths).Load(); err == nil || journal != nil {
				t.Fatalf("load invalid update journal = (%#v, %v), want (nil, error)", journal, err)
			}
			got, err := os.ReadFile(paths.UpdateJournal)
			if err != nil {
				t.Fatalf("invalid update journal should remain on disk: %v", err)
			}
			if string(got) != test.contents {
				t.Fatalf("invalid update journal changed: got %q, want %q", got, test.contents)
			}
		})
	}
}

func TestUpdateJournalSaveRejectsInvalidRecordWithoutReplacingExistingJournal(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	store := newUpdateJournalStore(paths)
	journal := validTestUpdateJournal(paths.GameRoot)
	if err := store.Save(journal); err != nil {
		t.Fatalf("save valid update journal: %v", err)
	}
	before, err := os.ReadFile(paths.UpdateJournal)
	if err != nil {
		t.Fatalf("read valid update journal: %v", err)
	}

	journal.GameRoot = filepath.Join(paths.GameRoot, "untrusted")
	if err := store.Save(journal); err == nil {
		t.Fatal("save journal with mismatched game root succeeded")
	}
	after, err := os.ReadFile(paths.UpdateJournal)
	if err != nil {
		t.Fatalf("read retained update journal: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("invalid save replaced the existing update journal")
	}
}

func TestUpdateJournalClear(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	store := newUpdateJournalStore(paths)
	if err := store.Save(validTestUpdateJournal(paths.GameRoot)); err != nil {
		t.Fatalf("save update journal: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("clear update journal: %v", err)
	}
	if _, err := os.Stat(paths.UpdateJournal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleared update journal stat error = %v, want not exist", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("clear missing update journal: %v", err)
	}
}

func TestUpdateJournalClearRetriesDirectorySyncWhenJournalIsAlreadyMissing(t *testing.T) {
	paths := makeDataPaths(t.TempDir())
	store := newUpdateJournalStore(paths)
	if err := store.Save(validTestUpdateJournal(paths.GameRoot)); err != nil {
		t.Fatal(err)
	}
	want := errors.New("injected directory sync failure")
	syncCalls := 0
	store.syncDirectory = func(string) error {
		syncCalls++
		if syncCalls == 1 {
			return want
		}
		return nil
	}

	if err := store.Clear(); !errors.Is(err, want) {
		t.Fatalf("first clear error = %v, want %v", err, want)
	}
	if _, err := os.Stat(paths.UpdateJournal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first clear did not remove the live journal: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("second clear did not retry directory sync: %v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want 2", syncCalls)
	}
}

func TestUpdateJournalPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	paths := makeDataPaths(t.TempDir())
	store := newUpdateJournalStore(paths)
	if err := store.Save(validTestUpdateJournal(paths.GameRoot)); err != nil {
		t.Fatalf("save update journal: %v", err)
	}
	info, err := os.Stat(paths.UpdateJournal)
	if err != nil {
		t.Fatalf("stat update journal: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("update journal permissions = %#o, want 0600", got)
	}
}

func validTestUpdateJournal(gameRoot string) updateJournal {
	return updateJournal{
		GameRoot:     gameRoot,
		FromCommit:   testUpdateFromCommit,
		TargetCommit: testUpdateTargetCommit,
		Strategy:     updateJournalStrategyInPlace,
		Phase:        updateJournalPhasePrepared,
	}
}

func quotedJSONPath(path string) string {
	contents, err := json.Marshal(path)
	if err != nil {
		panic(err)
	}
	return string(contents)
}
