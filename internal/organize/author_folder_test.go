package organize

import (
	"path/filepath"
	"strings"
	"testing"

	"audiobookrenamer/internal/model"
)

// A library chooses which form of the author's name names the author folder:
// the sort name ("Campbell, Jack") or the display name ("Jack Campbell").
func TestBuildPlan_AuthorFolderMode(t *testing.T) {
	cases := []struct {
		name string
		mode model.AuthorFolderMode
		want string
	}{
		{"default is the sort name", "", "Campbell, Jack"},
		{"sort name", model.AuthorFolderSort, "Campbell, Jack"},
		{"display name", model.AuthorFolderName, "Jack Campbell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			root := t.TempDir()
			lib, err := d.CreateLibrary(model.Library{
				Name: "L", RootPath: root,
				StructureMode:    model.AuthorFirst,
				AuthorFolderMode: tc.mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			b := matchedBook(t, d, lib,
				filepath.Join(root, "in"), filepath.Join(root, "in", "v.m4b"),
				model.LayoutSingle, []string{"v.m4b"},
				model.Book{
					Title: "Victorious", Author: "Jack Campbell",
					AuthorSort: "Campbell, Jack", Year: 2010,
				})

			plan, err := BuildPlan(d, lib.ID, []string{b.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Books) != 1 || len(plan.Books[0].Moves) != 1 {
				t.Fatalf("unexpected plan: %+v", plan.Books)
			}
			to := plan.Books[0].Moves[0].ToRel
			if !strings.HasPrefix(to, tc.want+"/") {
				t.Fatalf("destination %q, want it under %q", to, tc.want)
			}
		})
	}
}

// A book that has only one of the two names is still placeable whichever mode
// the library uses - the other name is the fallback, not a reason to skip.
func TestBuildPlan_AuthorFolderFallsBackToTheOtherName(t *testing.T) {
	cases := []struct {
		name string
		mode model.AuthorFolderMode
		meta model.Book
		want string
	}{
		{
			name: "sort mode with only a display name",
			mode: model.AuthorFolderSort,
			meta: model.Book{Title: "Victorious", Author: "Jack Campbell", Year: 2010},
			want: "Jack Campbell",
		},
		{
			name: "name mode with only a sort name",
			mode: model.AuthorFolderName,
			meta: model.Book{Title: "Victorious", AuthorSort: "Campbell, Jack", Year: 2010},
			want: "Campbell, Jack",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			root := t.TempDir()
			lib, err := d.CreateLibrary(model.Library{
				Name: "L", RootPath: root,
				StructureMode:    model.AuthorFirst,
				AuthorFolderMode: tc.mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			b := matchedBook(t, d, lib,
				filepath.Join(root, "in"), filepath.Join(root, "in", "v.m4b"),
				model.LayoutSingle, []string{"v.m4b"}, tc.meta)

			plan, err := BuildPlan(d, lib.ID, []string{b.ID})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Books[0].Skip {
				t.Fatalf("book skipped as authorless: %s", plan.Books[0].Reason)
			}
			if to := plan.Books[0].Moves[0].ToRel; !strings.HasPrefix(to, tc.want+"/") {
				t.Fatalf("destination %q, want it under %q", to, tc.want)
			}
		})
	}
}
