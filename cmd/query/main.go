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

	// Add go-better-auth to blacklist
	err = database.AddToBlacklist("go-better-auth/go-better-auth", "User requested")
	if err != nil {
		fmt.Println("Error adding to blacklist:", err)
	} else {
		fmt.Println("Added to blacklist: go-better-auth/go-better-auth")
	}

	// Show blacklist
	entries, _ := database.GetBlacklist()
	fmt.Printf("\n=== Blacklist (%d repos) ===\n", len(entries))
	for _, e := range entries {
		fmt.Printf("  %s | %s\n", e.Repo, e.Reason)
	}
}
