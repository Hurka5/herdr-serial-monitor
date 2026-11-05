package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Per-port persisted configuration: ~/.config/serial-broker/ports.toml
// (XDG_CONFIG_HOME honored, see DefaultConfigPath).
//
// The file holds one `port-id = baud` pair per line; `#` comments (full
// line or trailing) and blank lines are allowed. Keys are the *sanitized*
// port id — the same form used for log file names (SanitizePortID) —
// which is always a valid TOML bare key even when the raw by-id name
// contains colons. The broker matches both the raw and the sanitized id
// when looking a port up, and `serial set` writes the sanitized form, so
// either can be typed.

// DefaultConfigPath is the default per-port config file:
// $XDG_CONFIG_HOME/serial-broker/ports.toml or
// ~/.config/serial-broker/ports.toml.
func DefaultConfigPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "serial-broker", "ports.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "serial-broker/ports.toml"
	}
	return filepath.Join(home, ".config", "serial-broker", "ports.toml")
}

// ValidBaud reports whether baud is a rate termios can set.
func ValidBaud(baud int) bool {
	_, ok := baudBits[baud]
	return ok
}

// SupportedBauds lists the settable rates, ascending.
func SupportedBauds() []int {
	out := make([]int, 0, len(baudBits))
	for b := range baudBits {
		out = append(out, b)
	}
	sort.Ints(out)
	return out
}

// ParsePortsConfig parses the ports.toml subset described above. Strict
// on purpose: a typo'd line must fail loudly at load time, not silently
// leave a port running at the wrong baud.
func ParsePortsConfig(data []byte) (map[string]int, error) {
	cfg := make(map[string]int)
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("ports.toml line %d: want `id = baud`, got %q", i+1, line)
		}
		id := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if j := strings.IndexByte(val, '#'); j >= 0 { // trailing comment
			val = strings.TrimSpace(val[:j])
		}
		if id == "" {
			return nil, fmt.Errorf("ports.toml line %d: empty port id", i+1)
		}
		if id != SanitizePortID(id) {
			return nil, fmt.Errorf("ports.toml line %d: id %q must be the sanitized port id (allowed: letters, digits, '.', '_', '-')", i+1, id)
		}
		baud, err := strconv.Atoi(val)
		if err != nil || baud <= 0 {
			return nil, fmt.Errorf("ports.toml line %d: baud must be a positive number, got %q", i+1, val)
		}
		if !ValidBaud(baud) {
			return nil, fmt.Errorf("ports.toml line %d: unsupported baud %d (supported: %v)", i+1, baud, SupportedBauds())
		}
		if _, dup := cfg[id]; dup {
			return nil, fmt.Errorf("ports.toml line %d: duplicate entry for %q", i+1, id)
		}
		cfg[id] = baud
	}
	return cfg, nil
}

// FormatPortsConfig renders cfg deterministically (sorted ids, header
// comment) so config writes and reloads round-trip cleanly.
func FormatPortsConfig(cfg map[string]int) string {
	var b strings.Builder
	b.WriteString("# serial-broker per-port config: <port-id> = <baud>\n")
	b.WriteString("# ids are the sanitized names shown by `serial list`.\n")
	ids := make([]string, 0, len(cfg))
	for id := range cfg {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(&b, "%s = %d\n", id, cfg[id])
	}
	return b.String()
}

// SavePortsConfig writes the config atomically (temp file + rename in the
// same directory), so the daemon never reloads a half-written file. It
// creates the config directory if needed.
func SavePortsConfig(path string, cfg map[string]int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ports.toml-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(FormatPortsConfig(cfg)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loadConfig (re)reads the config file into b.configs. Called at startup
// and, from the discovery goroutine, whenever the file's mtime or size
// changed. A missing file is not an error; deleting the file returns all
// ports to the default baud. A malformed file keeps the previous
// settings (and is logged once per modification, since reloads only
// trigger on file changes). Applying new values to ports happens in scan
// via applyBaud.
func (b *Broker) loadConfig() {
	data, err := os.ReadFile(b.cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			if b.cfgSeen {
				b.cfgSeen = false
				b.mu.Lock()
				b.configs = map[string]int{}
				b.mu.Unlock()
				b.logf("config %s removed; ports fall back to %d baud", b.cfgPath, DefaultBaud)
			}
			return
		}
		b.logf("config %s: %v (keeping previous settings)", b.cfgPath, err)
		return
	}
	cfg, err := ParsePortsConfig(data)
	if err != nil {
		b.logf("config %s: %v (keeping previous settings)", b.cfgPath, err)
		return
	}
	if st, err := os.Stat(b.cfgPath); err == nil {
		b.cfgMod, b.cfgSize, b.cfgSeen = st.ModTime(), st.Size(), true
	}
	b.mu.Lock()
	b.configs = cfg
	b.mu.Unlock()
	b.logf("config %s: %d port(s) configured", b.cfgPath, len(cfg))
}

// maybeReloadConfig stats the config file and reloads on mtime/size
// change. Polling alongside discovery (every ~2s) is plenty for a config
// file; ext4 mtimes are nanosecond-granular, and the size check covers
// coarse-grained filesystems.
func (b *Broker) maybeReloadConfig() {
	st, err := os.Stat(b.cfgPath)
	if err != nil {
		if os.IsNotExist(err) && b.cfgSeen {
			b.loadConfig() // handles the deletion path
		}
		return
	}
	if b.cfgSeen && st.ModTime().Equal(b.cfgMod) && st.Size() == b.cfgSize {
		return
	}
	b.loadConfig()
}

// configuredEntryLocked looks up the configured baud for a port id under
// both the raw and the sanitized form (config keys are sanitized).
// Caller holds b.mu.
func (b *Broker) configuredEntryLocked(id string) (int, bool) {
	if v, ok := b.configs[id]; ok {
		return v, true
	}
	if v, ok := b.configs[SanitizePortID(id)]; ok {
		return v, true
	}
	return 0, false
}

// configuredBaudLocked is the baud a port should run at: the configured
// value if any, else DefaultBaud. Caller holds b.mu.
func (b *Broker) configuredBaudLocked(id string) int {
	if v, ok := b.configuredEntryLocked(id); ok {
		return v
	}
	return DefaultBaud
}

// applyBaud reconciles a port's baud with the configured value: an open
// port is closed and reopened with the new settings (so config changes
// take effect without a service restart; cursor and log continue where
// they were), a closed port just remembers it for its next attach.
func (b *Broker) applyBaud(p *Port, want int) {
	p.mu.Lock()
	if !p.open || p.baud == want {
		if !p.open {
			p.baud = want // next (lazy) attach opens at the new rate
		}
		p.mu.Unlock()
		return
	}
	old := p.baud
	p.baud = want
	freed, flashing := p.freed, p.flashing
	p.mu.Unlock()

	if flashing {
		return // flash()'s own reopen picks the new baud up
	}
	if freed {
		b.logf("port %s: baud %d → %d (freed; next read/write opens it at the new rate)", p.id, old, want)
		return
	}
	b.logf("port %s: baud %d → %d, reopening", p.id, old, want)
	p.closeDevice()
	if err := p.attach(); err != nil {
		b.logf("port %s: reopen at %d baud failed: %v", p.id, want, err)
	}
}
