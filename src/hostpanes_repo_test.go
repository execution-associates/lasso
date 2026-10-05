package main

import "testing"

func TestPaneRepoName(t *testing.T) {
	roots := []string{"/home/u/projects", "/home/u/projects/exa"}
	cases := []struct {
		rec, cwd, want string
	}{
		{"/home/u/projects/app", "/anywhere", "app"},
		{"", "/home/u/.lasso/worktrees/lasso/fix-a1b2/src", "lasso"},
		{"", "/home/u/.lasso/worktrees/lasso", "lasso"},
		{"", "/home/u/projects/exa/lasso/src/web", "lasso"},
		{"", "/home/u/projects/titan-iac", "titan-iac"},
		{"", "/home/u/projects", ""},
		{"", "/tmp/x", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := paneRepoName(c.rec, c.cwd, func() []string { return roots }); got != c.want {
			t.Errorf("paneRepoName(%q, %q) = %q, want %q", c.rec, c.cwd, got, c.want)
		}
	}
}
