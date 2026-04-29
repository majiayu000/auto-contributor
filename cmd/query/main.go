package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/majiayu000/auto-contributor/internal/db"
)

func main() {
	database, err := db.NewWithURL(os.Getenv("DATABASE_URL"), "/tmp/test.db")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer database.Close()

	if len(os.Args) < 2 {
		usage()
		return
	}

	repoFilter := ""
	if len(os.Args) > 2 {
		repoFilter = likePattern(os.Args[2])
	}

	switch os.Args[1] {
	case "repo_profiles":
		profiles, err := database.ListRepoProfiles(repoFilter)
		if err != nil {
			fmt.Println("Error listing repo profiles:", err)
			return
		}
		fmt.Printf("=== Repo Profiles (%d) ===\n", len(profiles))
		for _, profile := range profiles {
			fmt.Printf("%s | prs=%d | merge_rate=%.2f | blacklisted=%t\n",
				profile.Repo, profile.TotalPRsSubmitted, profile.MergeRate, profile.Blacklisted)
		}
	case "blacklist":
		entries, err := database.GetBlacklistFiltered(repoFilter)
		if err != nil {
			fmt.Println("Error listing blacklist:", err)
			return
		}
		fmt.Printf("=== Blacklist (%d repos) ===\n", len(entries))
		for _, entry := range entries {
			fmt.Printf("%s | %s\n", entry.Repo, entry.Reason)
		}
	default:
		usage()
	}
}

func likePattern(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return ""
	}
	return "%" + repo + "%"
}

func usage() {
	fmt.Println("usage: query <repo_profiles|blacklist> [repo-filter]")
}
