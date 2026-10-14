package agent

import (
	"fmt"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
)

// Catalog is the set of skills and agents a coordinator would have.
type Catalog struct {
	skills map[string]bool
	agents map[string]bool
}

// HasSkill reports whether name is an active skill.
func (c Catalog) HasSkill(name string) bool { return c.skills[name] }

// HasAgent reports whether name is an agent a workflow can run, which
// excludes the orchestrator.
func (c Catalog) HasAgent(name string) bool { return c.agents[name] }

// DiscoverCatalog discovers the skills and agents a coordinator built from
// cfg would have, the same way NewCoordinator does, without building one.
// It doesn't change cfg.
func DiscoverCatalog(cfg *config.ConfigStore) (Catalog, error) {
	plugins := plugin.DiscoverAll(cfg.Config().Plugins, nil)
	_, activeSkills, _ := discoverSkills(cfg, plugins)
	agentMDs, err := discoverAgentMDs(agentMDFS, plugins, nil)
	if err != nil {
		return Catalog{}, fmt.Errorf("loading agent descriptions: %w", err)
	}

	cat := Catalog{skills: map[string]bool{}, agents: map[string]bool{}}
	for _, s := range activeSkills {
		cat.skills[s.Name] = true
	}
	for name := range cfg.Config().SetupAgentsWithDefaults(agentDefaultsFromMDs(agentMDs)) {
		cat.agents[name] = name != config.AgentOrchestrator
	}
	return cat, nil
}
