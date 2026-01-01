package main

import (
	"fmt"
	"os"

	"github.com/majiayu000/auto-contributor/internal/db"
)

func main() {
	database, err := db.NewWithURL(os.Getenv("DATABASE_URL"), "/tmp/test.db")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer database.Close()

	// Add to blacklist
	repo := "external-secrets/external-secrets"
	err = database.AddToBlacklist(repo, "User requested")
	if err != nil {
		fmt.Println("Error adding to blacklist:", err)
		return
	}
	fmt.Printf("Added to blacklist: %s\n", repo)

	// Show current blacklist
	entries, _ := database.GetBlacklist()
	fmt.Printf("\n=== Blacklist (%d repos) ===\n", len(entries))
	for _, e := range entries {
		fmt.Printf("  %s | %s\n", e.Repo, e.Reason)
	}
}
