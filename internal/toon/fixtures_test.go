package toon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fixtureFile struct {
	Version string    `json:"version"`
	Tests   []fixture `json:"tests"`
}

type fixture struct {
	Name        string          `json:"name"`
	Input       json.RawMessage `json:"input"`
	Expected    *string         `json:"expected"`
	ShouldError bool            `json:"shouldError"`
	Options     struct {
		Delimiter  *string `json:"delimiter"`
		IndentSize *int    `json:"indentSize"`
	} `json:"options"`
}

// The mailbox encoder implements the comma/2-space subset (spec §7 of the
// mailbox spec). Fixtures requesting another delimiter or indent are skipped;
// the skip count is asserted so the suite can never silently skip everything.
func TestSpecEncodeFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/encode/*.json")
	if err != nil || len(files) != 9 {
		t.Fatalf("fixture files = %v (%v), want the 9 vendored files", files, err)
	}
	total, skipped := 0, 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var ff fixtureFile
		if err := json.Unmarshal(data, &ff); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for index, tc := range ff.Tests {
			total++
			if (tc.Options.Delimiter != nil && *tc.Options.Delimiter != ",") ||
				(tc.Options.IndentSize != nil && *tc.Options.IndentSize != 2) {
				skipped++
				continue
			}
			name := filepath.Base(file) + "/" + tc.Name
			t.Run(name, func(t *testing.T) {
				got, err := EncodeJSON(tc.Input)
				if tc.ShouldError {
					if err == nil {
						t.Fatalf("fixture %d: expected error, got %q", index, got)
					}
					return
				}
				if err != nil {
					t.Fatalf("fixture %d: %v", index, err)
				}
				if tc.Expected == nil {
					t.Fatalf("fixture %d: expected output is missing", index)
				}
				if got != *tc.Expected {
					t.Fatalf("fixture %d:\n got: %q\nwant: %q", index, got, *tc.Expected)
				}
			})
		}
	}
	if skipped == 0 || skipped >= total/2 {
		t.Fatalf("skipped %d of %d fixtures — expected only the non-comma/indent≠2 subset to skip", skipped, total)
	}
}
