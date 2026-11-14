package main

import (
	"context"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/pkg/selfupdate"
)

func (s *server) updateAvailable() string {
	v, _ := s.update.Load().(string)
	return v
}

// startAutoUpdate remembers (and announces to clients) a newer release found earlier, and, with
// auto_update on, checks for one at most every 24 hours and installs it when no session would end.
func (s *server) startAutoUpdate(ctx context.Context) {
	set := s.o.loadSettings(nil)
	path := selfupdate.StatePath(s.o.dataDir)
	s.update.Store(selfupdate.LoadState(path).AvailableFor(version))
	if !set.AutoUpdate || !selfupdate.ValidChannel(set.UpdateChannel) {
		return
	}
	if _, err := selfupdate.ParseVersion(version); err != nil {
		s.log.Info("auto-update off: development build")
		return
	}
	s.log.Info("auto-update on", "channel", set.UpdateChannel)
	go s.autoUpdateLoop(ctx, set.UpdateChannel, path, time.Hour)
}

func (s *server) autoUpdateLoop(ctx context.Context, channel, statePath string, every time.Duration) {
	tried := ""
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.autoUpdateOnce(ctx, channel, statePath, &tried)
		select {
		case <-ctx.Done():
			return
		case <-s.handedOver:
			return
		case <-t.C:
		}
	}
}

// autoUpdateOnce is one tick: a network check when 24h passed, then an install if something newer is
// known and nothing would be dropped. tried stops a failing version from being retried every hour.
func (s *server) autoUpdateOnce(ctx context.Context, channel, statePath string, tried *string) {
	u := newUpdater()
	st := selfupdate.LoadState(statePath)
	if st.Due(time.Now()) {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		res, _, err := u.Check(cctx, channel)
		cancel()
		st.LastCheck = time.Now().UnixMilli()
		if err != nil {
			s.log.Warn("update check failed", "err", err)
			_ = st.Save(statePath)
			return
		}
		st.Available = ""
		if res.Newer {
			st.Available = res.Latest
		}
		_ = st.Save(statePath)
	}
	avail := st.AvailableFor(version)
	s.update.Store(avail)
	if avail == "" || avail == *tried {
		return
	}
	s.mu.Lock()
	d := s.d
	s.mu.Unlock()
	if end := d.ReloadPlan().End; len(end) > 0 {
		s.log.Info("update available but not installed: a reload would end sessions", "version", avail, "sessions", len(end))
		return
	}
	*tried = avail
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, rel, err := u.Check(cctx, channel)
	if err != nil || !res.Newer {
		s.log.Warn("update: release lookup failed", "err", err)
		return
	}
	if _, err := u.Update(cctx, rel); err != nil {
		s.log.Warn("update failed", "version", avail, "err", err)
		return
	}
	s.log.Info("update installed, reloading", "version", res.Latest)
	if err := s.doReload(false, func(m string) { s.log.Info("reload: " + m) }); err != nil {
		s.log.Warn("update installed but not reloaded; it runs after the next restart", "err", err)
		return
	}
}
