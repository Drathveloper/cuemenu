package model

// EditKeys exposes the edited paths to the black-box tests.
func (c *Config) EditKeys() []string { return c.keys() }
