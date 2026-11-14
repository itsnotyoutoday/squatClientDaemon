package engine

import (
	"errors"
	"strings"
)

// RenameSession changes the name a connection goes by (its tab, and what scripts see). It is local:
// it changes nothing in the game, in a squatter, or in a world record. Use the app's rename, which
// also renames the saved world and the TinyFugue world record, for those.
func (e *Engine) RenameSession(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a name is required")
	}
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	s.mu.Lock()
	if s.info.Name == name {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	e.mu.Lock()
	tfName := e.uniqueName(name)
	e.mu.Unlock()
	s.mu.Lock()
	s.tfName = tfName
	s.info.Name = name
	s.world.Name = name
	s.publishInfoLocked()
	s.mu.Unlock()
	return nil
}
