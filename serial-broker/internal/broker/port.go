package broker

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultBaud is the baud every port runs at unless ports.toml says
	// otherwise: 115200 8N1, no flow control.
	DefaultBaud = 115200
	// A partial line (no newline yet) is emitted after this much silence,
	// so devices that never send newlines still stream.
	flushQuiet = 100 * time.Millisecond
	flushTick  = 50 * time.Millisecond // how often we check for quiet
)

// PortConfig overrides device settings for one port id (keyed by the
// canonical port id). Zero value → broker defaults (115200 8N1, no flow
// control). Scaffolding for later per-port configuration.
type PortConfig struct {
	Baud int
}

// baudBits maps supported rates to termios cflag bits.
var baudBits = map[int]uint32{
	9600:   unix.B9600,
	19200:  unix.B19200,
	38400:  unix.B38400,
	57600:  unix.B57600,
	115200: unix.B115200,
	230400: unix.B230400,
	460800: unix.B460800,
	921600: unix.B921600,
}

// Port is one serial port the broker knows about. All mutable fields are
// guarded by mu.
type Port struct {
	b  *Broker
	id string // canonical identity (by-id basename or device basename)

	mu       sync.Mutex
	device   string // current device path (by-id resolution can change on replug)
	freed    bool   // Free()d: discovery stops re-attaching until a read/write
	flashing bool   // a flash subprocess currently owns the device
	open     bool
	baud     int
	file     *os.File // device fd, nil while closed
	devFD    int      // raw fd of file, -1 while closed (for TIOCNXCL at close)

	// log state (see logfile.go)
	logPath       string
	logFile       *os.File
	logInit       bool
	cursor        int64     // last cursor assigned
	lastAttachLog time.Time // rate-limits repeated attach-failure logging

	buf    []byte    // partial-line accumulator of the read framer
	lastRx time.Time // when the last bytes arrived

	closing   atomic.Bool // distinguishes deliberate close from device error in the read loop
	stop      chan struct{}
	readDone  chan struct{}
	flushDone chan struct{}
}

// attach opens the device unless it is already open, a flash is in
// progress, or it was freed. read/write call attach directly, which is
// the lazy re-attach that undoes a Free.
func (p *Port) attach() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.flashing {
		return fmt.Errorf("port %s: flash in progress", p.id)
	}
	if p.open {
		return nil
	}
	return p.openDeviceLocked()
}

// openDeviceLocked opens the port exclusively in raw mode and starts the
// read/flush goroutines. Caller holds p.mu.
func (p *Port) openDeviceLocked() error {
	path := p.device
	// O_NONBLOCK matters twice: the open cannot hang on modem lines, and
	// it makes os.NewFile register the fd with the Go runtime poller.
	//
	// The fd KEEPS O_NONBLOCK on purpose. The poller turns EAGAIN into
	// ordinary blocking semantics for Read/Write ("set blocking" in
	// effect), and — crucially — it lets file.Close() from another
	// goroutine interrupt a pending Read. With a literally blocking fd
	// the read loop would sit in an uninterruptible kernel read on Linux
	// (close(2) does not wake a blocked reader) and Free/flash/shutdown
	// would hang forever.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	// TIOCEXCL: every other open(2) on this device fails with EBUSY.
	// This is the whole point of the broker — nobody else reads the port
	// and splits incoming bytes between processes.
	if err := unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil {
		unix.Close(fd)
		return fmt.Errorf("TIOCEXCL %s: %w", path, err)
	}
	if err := setRawTermios(fd, p.baud); err != nil {
		unix.Close(fd)
		return fmt.Errorf("termios %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)

	p.file = f
	p.devFD = fd
	p.open = true
	p.freed = false
	p.buf = nil
	p.closing.Store(false)
	p.stop = make(chan struct{})
	p.readDone = make(chan struct{})
	p.flushDone = make(chan struct{})
	go p.readLoop(f)
	go p.flushLoop()
	p.b.logf("attached %s (%s) @ %d baud", p.id, path, p.baud)
	return nil
}

// setRawTermios puts the fd in raw mode, 8N1, no flow control, and sets
// the baud rate via the classic CBAUD cflag bits (fine for standard
// rates on Linux).
func setRawTermios(fd int, baud int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	// cfmakeraw equivalent: let bytes cross untouched.
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	// 8 data bits (CS8), 1 stop bit (no CSTOPB), no parity (no PARENB),
	// no RTS/CTS or XON/XOFF flow control (no CRTSCTS, IXON cleared above).
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	bits, ok := baudBits[baud]
	if !ok {
		return fmt.Errorf("unsupported baud rate %d", baud)
	}
	t.Cflag = (t.Cflag &^ unix.CBAUD) | bits
	t.Cc[unix.VMIN] = 1  // return as soon as ≥1 byte is available;
	t.Cc[unix.VTIME] = 0 // partial-line framing with its ~100ms flush is done in userspace
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}

// readLoop frames device bytes into log lines: a Read blocks until data
// arrives (poller), complete lines are logged immediately, partial ones
// by flushLoop after ~100ms of silence.
//
// The fd arrives as a parameter, not via p.file: closeDevice nils p.file
// under p.mu from another goroutine, and the read loop must not touch it.
func (p *Port) readLoop(f *os.File) {
	defer close(p.readDone)
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.buf = append(p.buf, buf[:n]...)
			p.lastRx = time.Now()
			p.drainBufLocked()
			p.mu.Unlock()
		}
		if err != nil {
			if p.closing.Load() {
				return // deliberate close (free/flash/shutdown)
			}
			// Device error — typically an unplug (EIO). Drop ownership;
			// discovery re-attaches if the device comes back.
			p.b.logf("read %s: %v — detaching", p.id, err)
			p.closeDevice()
			return
		}
	}
}

// drainBufLocked emits every complete line currently in the buffer.
func (p *Port) drainBufLocked() {
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			return
		}
		line := string(p.buf[:i])
		p.buf = p.buf[i+1:]
		if _, err := p.appendLineLocked('R', TrimLineEnding(line)); err != nil {
			p.b.logf("log append %s: %v", p.id, err)
		}
	}
}

// flushLoop emits the pending partial line once traffic went quiet, so
// devices that never send newlines still stream.
func (p *Port) flushLoop() {
	defer close(p.flushDone)
	stop := p.stop // snapshot: the field is replaced on reopen, after we joined
	t := time.NewTicker(flushTick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.mu.Lock()
			if len(p.buf) > 0 && time.Since(p.lastRx) >= flushQuiet {
				if _, err := p.appendLineLocked('R', TrimLineEnding(string(p.buf))); err != nil {
					p.b.logf("log append %s: %v", p.id, err)
				}
				p.buf = nil
			}
			p.mu.Unlock()
		}
	}
}

// closeDevice stops owning the port: it flushes the pending partial line
// (no bytes lost), stops the read/flush goroutines and closes the fd.
// Closing the fd is what releases the TIOCEXCL claim, so other programs
// can open the device again. Idempotent and safe to call concurrently.
//
// Locking: only the state flip happens under p.mu; the goroutine joins
// happen after releasing it, so a read loop mid-frame can finish its
// append without deadlocking.
func (p *Port) closeDevice() {
	p.mu.Lock()
	if !p.open {
		p.mu.Unlock()
		return
	}
	p.closing.Store(true)
	if len(p.buf) > 0 { // don't lose a partial line at close
		if _, err := p.appendLineLocked('R', TrimLineEnding(string(p.buf))); err != nil {
			p.b.logf("log append %s: %v", p.id, err)
		}
		p.buf = nil
	}
	f := p.file
	fd := p.devFD
	p.file = nil
	p.devFD = -1
	p.open = false
	p.mu.Unlock()

	close(p.stop) // stop flushLoop
	// Drop the exclusive claim explicitly: for a pts (test rig) whose
	// master is still open, close(2) alone does NOT clear TIOCEXCL and
	// every later open fails with EBUSY. Harmless on real serial devices.
	unix.IoctlSetInt(fd, unix.TIOCNXCL, 0)
	f.Close() // interrupts readLoop's pending Read (poller-registered fd)
	<-p.readDone
	<-p.flushDone
	p.b.logf("detached %s", p.id)
}
