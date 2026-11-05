// Package broker owns serial ports exclusively and multiplexes readers.
package broker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Broker design (see README.md):
//
//   - discovery: watch /dev/serial/by-id/* (stable across replug) plus
//     ttyUSB*/ttyACM*; the by-id path is the canonical port identity.
//   - ownership: open with TIOCEXCL in raw mode so nothing else can read
//     a port and split incoming bytes. Baud/config per port, defaults
//     sensible for embedded dev (115200 8N1), overridable.
//   - log: append-only file per port, one line per framed read:
//     "<cursor> <unix-ms> <R|W> <text>" — W lines are our own writes,
//     echoed so a reader sees the whole conversation; F lines are flash
//     subprocess output.
//   - read path: `read {port, since}` returns lines with cursor > since
//     and the next cursor; that is the whole multiplexing story.
//   - flash: close port -> run flasher argv -> reopen. Reopen toggles DTR
//     (board reset) and the boot log lands in the log.
type Broker struct {
	dataDir string
	byIDDir string // /dev/serial/by-id (overridable for tests)
	devDir  string // /dev (overridable for tests)
	extra   []string
	poll    time.Duration
	logf    func(format string, args ...any)

	cfgPath string
	// configs maps config key (sanitized port id) → baud; guarded by mu.
	// The file is the source of truth: loadConfig replaces it wholesale.
	configs map[string]int
	// config file state, only touched by the discovery goroutine:
	cfgMod  time.Time
	cfgSize int64
	cfgSeen bool // a config file was loaded (so deletion can be noticed)

	mu    sync.Mutex // guards ports
	ports map[string]*Port

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// Options configures a Broker.
type Options struct {
	// DataDir is the root for logs: <DataDir>/logs/<port-id>.log
	// (default ~/.local/share/serial-broker, see DefaultDataDir).
	DataDir string
	// ByIDDir and DevDir redirect discovery, normally /dev/serial/by-id
	// and /dev. Tests point them at fake directory trees.
	ByIDDir string
	DevDir  string
	// ExtraPorts are device paths to also own (id = basename), e.g. a
	// PTY handed to the broker explicitly.
	ExtraPorts []string
	// PollInterval is the discovery poll period (default 2s; no inotify,
	// plain polling is plenty for plugging in boards). The per-port
	// config file is re-checked on the same tick.
	PollInterval time.Duration
	// ConfigPath overrides the per-port config file location (default
	// DefaultConfigPath, normally ~/.config/serial-broker/ports.toml).
	// See config.go for the format.
	ConfigPath string
	// Configs seeds per-port baud settings (programmatic scaffolding).
	// A present config file replaces this wholesale — the file is the
	// runtime source of truth, and deleting it returns ports to
	// DefaultBaud.
	Configs map[string]PortConfig
	// Logf receives diagnostics (default: discard).
	Logf func(format string, args ...any)
}

// DefaultDataDir is the default log root: $XDG_DATA_HOME/serial-broker
// or ~/.local/share/serial-broker.
func DefaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "serial-broker")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "serial-broker"
	}
	return filepath.Join(home, ".local", "share", "serial-broker")
}

// New starts a Broker: discovery runs immediately, then every interval.
func New(opt Options) *Broker {
	if opt.PollInterval <= 0 {
		opt.PollInterval = 2 * time.Second
	}
	if opt.ByIDDir == "" {
		opt.ByIDDir = "/dev/serial/by-id"
	}
	if opt.DevDir == "" {
		opt.DevDir = "/dev"
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.ConfigPath == "" {
		opt.ConfigPath = DefaultConfigPath()
	}
	// Options.Configs is a programmatic seed; the config file (if any)
	// replaces it on the synchronous load below.
	seed := make(map[string]int)
	for id, c := range opt.Configs {
		if c.Baud != 0 {
			seed[id] = c.Baud
		}
	}
	b := &Broker{
		dataDir: opt.DataDir,
		byIDDir: opt.ByIDDir,
		devDir:  opt.DevDir,
		extra:   opt.ExtraPorts,
		poll:    opt.PollInterval,
		cfgPath: opt.ConfigPath,
		configs: seed,
		logf:    opt.Logf,
		ports:   make(map[string]*Port),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	b.loadConfig() // synchronous: the first attach uses the right baud
	go b.discoverLoop()
	return b
}

// discoverLoop rescans the device tree and re-checks the per-port config
// file every poll interval.
func (b *Broker) discoverLoop() {
	defer close(b.done)
	b.maybeReloadConfig()
	b.scan()
	t := time.NewTicker(b.poll)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.maybeReloadConfig()
			b.scan()
		}
	}
}

// Dev is one discovered device.
type Dev struct {
	ID     string // canonical port id (by-id basename, or device basename)
	Device string // real path after symlink resolution
}

// ScanDevs enumerates the ports to own:
//
//   - entries under byIDDir (normally /dev/serial/by-id) are the
//     canonical identity, stable across replug; each symlink is resolved
//     to the real /dev path;
//   - devDir/ttyUSB* and devDir/ttyACM* that no by-id entry points at
//     (id = basename);
//   - extra paths (--port flag), id = basename.
//
// A device is claimed only once; by-id is scanned first, so it defines
// the identity.
func ScanDevs(byIDDir, devDir string, extra []string) []Dev {
	var devs []Dev
	claimed := map[string]bool{} // resolved device path → already claimed

	if ents, err := os.ReadDir(byIDDir); err == nil {
		for _, e := range ents {
			real, err := filepath.EvalSymlinks(filepath.Join(byIDDir, e.Name()))
			if err != nil {
				continue // dangling symlink: replug in progress
			}
			devs = append(devs, Dev{ID: e.Name(), Device: real})
			claimed[real] = true
		}
	}
	for _, pat := range []string{"ttyUSB*", "ttyACM*"} {
		paths, _ := filepath.Glob(filepath.Join(devDir, pat))
		for _, path := range paths {
			real, err := filepath.EvalSymlinks(path)
			if err != nil || claimed[real] {
				continue
			}
			claimed[real] = true
			devs = append(devs, Dev{ID: filepath.Base(path), Device: real})
		}
	}
	for _, path := range extra {
		real, err := filepath.EvalSymlinks(path)
		if err != nil || claimed[real] {
			continue // not present (yet); retried next poll
		}
		claimed[real] = true
		devs = append(devs, Dev{ID: filepath.Base(path), Device: real})
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].ID < devs[j].ID })
	return devs
}

// scan merges the current device set into the port table and (re)attaches
// ports that should be owned. Ports are never removed once seen: an
// unplugged port just shows open=false, and because the by-id identity is
// stable, a replug re-attaches with the same log and cursor.
func (b *Broker) scan() {
	for _, d := range ScanDevs(b.byIDDir, b.devDir, b.extra) {
		b.mu.Lock()
		want := b.configuredBaudLocked(d.ID)
		p, seen := b.ports[d.ID]
		if !seen {
			p = &Port{b: b, id: d.ID, device: d.Device, baud: want, devFD: -1}
			b.ports[d.ID] = p
		}
		b.mu.Unlock()

		p.mu.Lock()
		p.device = d.Device // replug may change the /dev path under a stable id
		open, freed, flashing := p.open, p.freed, p.flashing
		p.mu.Unlock()

		if !seen {
			// Read any existing log now so list/read report the true
			// cursor even before new traffic arrives.
			p.mu.Lock()
			p.ensureLogLocked()
			p.mu.Unlock()
		}
		// Reconcile the configured baud: reopens the port if it changed
		// (cursor/log continue where they were).
		b.applyBaud(p, want)
		if !open && !freed && !flashing {
			// Best effort; retried next poll (device may be busy or gone).
			// Failure logging is rate-limited — a permanently busy port
			// must not spam the journal every 2 seconds.
			if err := p.attach(); err != nil {
				p.mu.Lock()
				quiet := time.Since(p.lastAttachLog) < time.Minute
				p.lastAttachLog = time.Now()
				p.mu.Unlock()
				if !quiet {
					b.logf("attach %s (%s): %v", d.ID, d.Device, err)
				}
			}
		}
	}
}

func (b *Broker) port(id string) (*Port, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.ports[id]
	if !ok {
		return nil, fmt.Errorf("unknown port %q — not discovered; see list", id)
	}
	return p, nil
}

// PortInfo is one row of the list response.
type PortInfo struct {
	Port       string `json:"port"`
	Device     string `json:"device"`
	Open       bool   `json:"open"`
	Baud       int    `json:"baud"`
	LastCursor int64  `json:"lastCursor"`
}

// List describes all known ports, sorted by id.
func (b *Broker) List() []PortInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]PortInfo, 0, len(b.ports))
	for _, p := range b.ports {
		p.mu.Lock()
		// Show the configured rate when one exists (even if the reopen is
		// still pending or failed); otherwise the active one.
		baud := p.baud
		if v, ok := b.configuredEntryLocked(p.id); ok {
			baud = v
		}
		out = append(out, PortInfo{
			Port: p.id, Device: p.device, Open: p.open,
			Baud: baud, LastCursor: p.cursor,
		})
		p.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// Read returns all log lines with cursor > since, plus the next cursor to
// pass back in. Reading a freed port lazily re-attaches it; if the device
// is gone, the stored history is still served.
func (b *Broker) Read(portID string, since int64) ([]string, int64, error) {
	p, err := b.port(portID)
	if err != nil {
		return nil, 0, err
	}
	p.mu.Lock()
	flashing := p.flashing
	p.mu.Unlock()
	if !flashing {
		p.attach() // best effort: history is served regardless
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.linesSinceLocked(since)
}

// Write sends data to the device and appends a W line to the log so
// readers see the whole conversation. Writing a freed port lazily
// re-attaches it.
func (b *Broker) Write(portID, data string) (int, error) {
	p, err := b.port(portID)
	if err != nil {
		return 0, err
	}
	if err := p.attach(); err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.open {
		return 0, fmt.Errorf("port %s is not open", portID)
	}
	n, err := p.file.WriteString(data)
	if n > 0 {
		if _, lerr := p.appendLineLocked('W', TrimLineEnding(data)); lerr != nil {
			b.logf("log append %s: %v", p.id, lerr)
		}
	}
	return n, err
}

// FlashResult reports what a flash subprocess did.
type FlashResult struct {
	ExitCode   int
	Output     string // tail (last ~4 KiB) of combined stdout+stderr
	NextCursor int64
}

// Flash closes the port (dropping the fd releases the TIOCEXCL claim, so
// the flasher can open the device itself), runs argv with stdout and
// stderr teed into the port log as F lines, waits for it to exit, then
// reopens the port.
//
// Reopening a USB CDC port toggles DTR, which resets most boards — that
// is a feature: the post-flash boot log lands in the log like any other
// output and no reader misses it.
func (b *Broker) Flash(portID string, argv []string) (res FlashResult, err error) {
	if len(argv) == 0 {
		return res, errors.New("flash: empty argv")
	}
	p, err := b.port(portID)
	if err != nil {
		return res, err
	}
	p.mu.Lock()
	if p.flashing {
		p.mu.Unlock()
		return res, fmt.Errorf("port %s: flash already in progress", portID)
	}
	p.flashing = true
	p.mu.Unlock()
	p.closeDevice()

	defer func() {
		p.mu.Lock()
		p.flashing = false
		freed := p.freed
		p.mu.Unlock()
		// Take the port back (which resets DTR-sensitive boards — see
		// above) unless somebody freed it while the flasher ran.
		if !freed {
			if err := p.attach(); err != nil {
				b.logf("flash: reopen %s: %v", p.id, err)
			}
		}
		p.mu.Lock()
		res.NextCursor = p.cursor
		p.mu.Unlock()
	}()

	cmd := exec.Command(argv[0], argv[1:]...)
	tee := &flashTee{p: p}
	cmd.Stdout, cmd.Stderr = tee, tee
	runErr := cmd.Run()
	tee.flush() // trailing partial line

	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return res, fmt.Errorf("flash %s: %w", portID, runErr)
	}
	if exitErr != nil {
		res.ExitCode = exitErr.ExitCode()
	}
	res.Output = tee.tailString()
	return res, nil
}

// Free closes the port and stops owning it so external tools (another
// monitor, a manual flasher) can open it. Discovery stops re-attaching
// it; the next read or write lazily re-attaches.
func (b *Broker) Free(portID string) error {
	p, err := b.port(portID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.freed = true
	p.mu.Unlock()
	p.closeDevice()
	return nil
}

// Set changes a port's baud rate: it validates the rate, remembers and
// persists it to the config file (so it survives restarts), and applies
// it immediately — an attached port is closed and reopened at the new
// rate (which resets most boards, same as the flash flow), a freed or
// flashing one picks it up on its next attach. Configuring a port that
// is not currently discovered is allowed; the setting applies when it
// appears.
func (b *Broker) Set(portID string, baud int) error {
	if !ValidBaud(baud) {
		return fmt.Errorf("unsupported baud %d (supported: %v)", baud, SupportedBauds())
	}

	// Read-modify-write of the whole config, under mu so concurrent Sets
	// (or a config reload) cannot interleave with the snapshot. Saving is
	// a small atomic rename; holding mu over it is fine.
	b.mu.Lock()
	cfg := make(map[string]int, len(b.configs)+1)
	for id, v := range b.configs {
		cfg[id] = v
	}
	cfg[SanitizePortID(portID)] = baud
	if err := SavePortsConfig(b.cfgPath, cfg); err != nil {
		b.mu.Unlock()
		return fmt.Errorf("persist %s: %w", b.cfgPath, err)
	}
	b.configs = cfg
	b.mu.Unlock()

	p, err := b.port(portID)
	if err != nil {
		return nil // not discovered (yet); scan applies it on discovery
	}
	b.applyBaud(p, baud)
	return nil
}

// Close stops discovery and closes every owned port cleanly (this is the
// SIGINT/SIGTERM path). Idempotent.
func (b *Broker) Close() {
	b.closeOnce.Do(func() {
		close(b.stop)
		<-b.done
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.ports {
		p.closeDevice()
		p.mu.Lock()
		if p.logFile != nil {
			p.logFile.Close()
			p.logFile = nil
			p.logInit = false
		}
		p.mu.Unlock()
	}
}
