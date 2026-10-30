// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"strings"
	"testing"
)

func goodConfig() Config {
	return Config{Endpoint: "https://aw.example/", ProjectID: "p", TeamID: "t", APIKey: "standard_teststub", Origin: "https://gable.example", AuthMode: ""}
}

func TestConfigCheck(t *testing.T) {
	if err := (Config{}).Check(); err != nil {
		t.Fatalf("no settings at all is a valid core without Appwrite: %v", err)
	}
	if err := goodConfig().Check(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Config){
		"dev beside settings": func(c *Config) { c.AuthMode = "dev" },
		"missing project":     func(c *Config) { c.ProjectID = "" },
		"missing team":        func(c *Config) { c.TeamID = "" },
		"missing key":         func(c *Config) { c.APIKey = "" },
		"missing endpoint":    func(c *Config) { c.Endpoint = "" },
		"missing origin":      func(c *Config) { c.Origin = "" },
		"bad endpoint":        func(c *Config) { c.Endpoint = "aw.example" },
		"bad origin":          func(c *Config) { c.Origin = "ftp://gable.example" },
	}
	for name, mutate := range cases {
		c := goodConfig()
		mutate(&c)
		err := c.Check()
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if strings.Contains(err.Error(), c.APIKey) && c.APIKey != "" {
			t.Errorf("%s: the error echoes the key", name)
		}
	}
}
