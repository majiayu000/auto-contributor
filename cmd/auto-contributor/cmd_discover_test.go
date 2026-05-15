package main

import (
	"testing"

	"github.com/majiayu000/auto-contributor/internal/config"
)

func TestSmartDiscoverSaveFilterConfigUsesRequestedMinStars(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	cfg = config.Default()
	cfg.MinRepoStars = 1000
	cfg.Languages = []string{"go"}
	cfg.ExcludeRepos = []string{"owner/excluded"}

	filterCfg := smartDiscoverSaveFilterConfig(50)
	if filterCfg.MinStars != 50 {
		t.Fatalf("MinStars=%d want requested min-stars %d", filterCfg.MinStars, 50)
	}
	if filterCfg.MinStars == cfg.MinRepoStars {
		t.Fatalf("MinStars reused cfg.MinRepoStars=%d", cfg.MinRepoStars)
	}
}
