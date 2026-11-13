package engine

// SetSessionWorld links a session to a saved world (used after a quick connect is saved as a world),
// so the session is listed as that world's from now on.
func (e *Engine) SetSessionWorld(id, worldID string) error {
	s := e.get(id)
	if s == nil {
		return ErrNoSession
	}
	s.mu.Lock()
	s.world.ID = worldID
	s.publishInfoLocked()
	s.mu.Unlock()
	return nil
}
