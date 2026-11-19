package model

import "cuelang.org/go/cue"

// EditKeys exposes the edited paths to the black-box tests.
func (c *Config) EditKeys() []string { return c.keys() }

// ExportValue is the configuration as exported, fields in schema order.
func (c *Config) ExportValue() cue.Value { return c.exportValue() }
