package immterm

import (
	"fmt"
	"io"
	"sync"

	"github.com/Kodecable/crosspty"
	"github.com/Kodecable/immterm/internal/midterm"
)

var (
	defaultHistorySize      = 4096 // Bytes
	defaultHistoryThreshold = 0.6  // Percentage (0.0 - 1.0)
)

type CommandConfig = crosspty.CommandConfig
type CloseConfig = crosspty.CloseConfig
type TermSize = crosspty.TermSize

type FetchResult struct {
	Screen  []string
	History []byte
	Missed  int
}

type OnThresholdFunc = func()

type OnThresholdWithFetchFunc = func(FetchResult)

type HistoryConfig struct {
	// Size is the maximum number of bytes in the history buffer.
	Size int

	// Threshold is the buffer usage percentage (0.0 - 1.0) that triggers callbacks.
	// Set to >= 1.0 to disable auto-triggering.
	Threshold float32

	// OnThreshold is called synchronously when the buffer fills to Threshold.
	// This blocks the terminal input/output processing.
	// You NEED to consume history, or you will be called when next line rolled.
	// Do not perform heavy operations or call Term methods here.
	OnThreshold OnThresholdFunc

	// This callback will be called in a new goroutine. Totally thread-safe.
	// Fetch always CLEAR history.
	OnThresholdWithFetchCallback OnThresholdWithFetchFunc
}

type Term struct {
	mterm *midterm.Terminal
	ptmx  crosspty.Pty

	mu      sync.Mutex
	history []byte
	size    int
	head    int
	tail    int
	full    bool
	missed  int

	triggerCount         int
	onThreshold          OnThresholdFunc
	onThresholdWithFetch OnThresholdWithFetchFunc

	ptmx2mtermWG  sync.WaitGroup
	ptmx2mtermErr error

	closer sync.Once
}

func normalizeHistoryConfig(hc *HistoryConfig) (triggerCount int) {
	if hc.Size <= 0 {
		hc.Size = defaultHistorySize
	}
	if hc.Threshold <= 0 {
		hc.Threshold = float32(defaultHistoryThreshold)
	}

	if hc.Threshold >= 1 {
		triggerCount = hc.Size + 1
		hc.OnThreshold = nil
		hc.OnThresholdWithFetchCallback = nil
	} else {
		triggerCount = int(hc.Threshold * float32(hc.Size))
	}

	return
}

func Start(cc CommandConfig, hc HistoryConfig) (*Term, error) {
	triggerCount := normalizeHistoryConfig(&hc)
	cc, err := crosspty.NormalizeCommandConfig(cc)
	if err != nil {
		return nil, err
	}

	mterm := midterm.NewTerminal(int(cc.Size.Rows), int(cc.Size.Cols))
	mterm.Raw = true
	mterm.CursorVisible = false

	t := &Term{
		mterm:   mterm,
		history: make([]byte, hc.Size),

		size:         hc.Size,
		triggerCount: triggerCount,

		onThreshold:          hc.OnThreshold,
		onThresholdWithFetch: hc.OnThresholdWithFetchCallback,
	}
	mterm.OnScrollback(t.onScrollback)

	ptmx, err := crosspty.Start(cc)
	if err != nil {
		return nil, err
	}
	t.ptmx = ptmx
	mterm.ForwardResponses = ptmx
	t.ptmx2mtermWG.Go(t.ptmx2mterm)

	return t, nil
}

func (t *Term) ptmx2mterm() {
	defer func() {
		if e := recover(); e != nil {
			if e, ok := e.(error); ok {
				t.ptmx2mtermErr = e
			} else {
				t.ptmx2mtermErr = fmt.Errorf("immterm: ptmx2mterm copy panic: %v", e)
			}
		}
	}()

	_, t.ptmx2mtermErr = io.Copy(t.mterm, t.ptmx)
}

// Thread-safe.
func (t *Term) SetCloseConfig(config CloseConfig) (err error) {
	return t.ptmx.SetCloseConfig(crosspty.CloseConfig(config))
}

// Thread-safe. safe for re-entry
func (t *Term) Close() (err error) {
	t.closer.Do(func() {
		err = t.ptmx.Close()
	})
	return
}

// Works. Not recommended.
// Thread-safe.
func (t *Term) Resize(sz TermSize) error {
	if err := t.ptmx.SetSize(sz); err != nil {
		return err
	}

	// midterm will lock itself
	// no need to lock Term
	t.mterm.Resize(int(sz.Rows), int(sz.Cols))
	return nil
}

func (t *Term) Wait() (exitCode int, ptyReadErr error) {
	exitCode = t.ptmx.Wait()
	t.ptmx2mtermWG.Wait()
	ptyReadErr = t.ptmx2mtermErr
	return
}

func (t *Term) Write(d []byte) (int, error) {
	return t.ptmx.Write(d)
}

func (t *Term) onScrollback(row int, ltc midterm.LockedTerminalContext) {
	t.mu.Lock()
	defer t.mu.Unlock()

	used := t.usedBytes()

	// call callback before save rolled line
	// avoid history and screen tearing
	if used > t.triggerCount {
		if t.onThreshold != nil {
			t.onThreshold()
		}
		if t.onThresholdWithFetch != nil {
			out := t.unsafeFetch(ltc)
			go t.onThresholdWithFetch(out)
		}
	}

	line := ltc.PlainRenderLine(row)
	lineBytes := make([]byte, len(line)+2)
	copy(lineBytes, line)
	lineBytes[len(lineBytes)-2] = '\r'
	lineBytes[len(lineBytes)-1] = '\n'
	t.appendHistory(lineBytes)
}

// This func will CLEAR history.
// thread-safe.
func (t *Term) DropHistory() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.missed = 0
	t.head = t.tail
	t.full = false
}

// Fetch the screen and history.
// This func will CLEAR history.
// thread-safe.
func (t *Term) Fetch() (out FetchResult) {
	// Lock mterm before Term, in case of dead lock
	unlock, ltc := t.mterm.Lock()
	defer unlock()
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.unsafeFetch(ltc)
}

func (t *Term) unsafeFetch(ltc midterm.LockedTerminalContext) (out FetchResult) {
	used := t.usedBytes()
	out.History = make([]byte, used)
	if used > 0 {
		if t.full {
			copy(out.History, t.history[t.head:])
			copy(out.History[t.size-t.head:], t.history[:t.tail])
		} else if t.tail >= t.head {
			copy(out.History, t.history[t.head:t.head+used])
		} else {
			first := copy(out.History, t.history[t.head:])
			copy(out.History[first:], t.history[:t.tail])
		}
	}

	out.Missed = t.missed
	t.missed = 0
	t.head = t.tail
	t.full = false

	out.Screen = ltc.PlainRender()
	return
}

func (t *Term) usedBytes() int {
	if t.full {
		return t.size
	}
	if t.tail >= t.head {
		return t.tail - t.head
	}
	return t.size - (t.head - t.tail)
}

func (t *Term) appendHistory(b []byte) {
	if t.size == 0 {
		return
	}

	need := len(b)
	if need > t.size {
		// midterm should splited line to term width
		// this is a wired bug or buf too small, reject this line
		t.missed++
		return
	}

	// ensure space
	for t.size-t.usedBytes() < need {
		t.dropOneLine()
	}

	// write with wrap
	n := copy(t.history[t.tail:], b)
	if n < need {
		copy(t.history, b[n:])
	}
	t.tail = (t.tail + need) % t.size
	if t.tail == t.head {
		t.full = true
	}
}

func (t *Term) dropOneLine() {
	if t.usedBytes() == 0 {
		return
	}
	foundCR := false
	for t.usedBytes() > 0 {
		ch := t.history[t.head]
		t.head = (t.head + 1) % t.size
		if t.full {
			t.full = false
		}
		if foundCR && ch == '\n' {
			break
		}
		foundCR = ch == '\r'
	}
	t.missed++
}
