package aqua

import (
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Status colours, matched to coolerlink's iCUE fan status effect so the whole loop reads as
// one system: calm white at idle, red under GPU load.
var (
	StatusIdle = [3]byte{255, 255, 255}
	// Busy = the rain's ORANGE: the case status rains red/orange during GPU work
	// (2026-08-16 redesign, was blue), so the pump and flow meter glow orange with
	// it. {255,90,0} matches the screen ring's "working" accent.
	StatusBusy = [3]byte{255, 90, 0}
)

// StatusGPUThreshold is the (EMA-smoothed) GPU utilisation (%) above which the loop counts as
// "busy". Matches the onset of coolerlink's load-proportional status rain.
const StatusGPUThreshold = 20

// gpuUtil returns GPU 0's utilisation (%), or -1 if unavailable. GPU 0 is the non-display
// card on this box, so desktop/video work can't fake a busy loop — the same signal
// coolerlink's status rain keys off.
func gpuUtil() int {
	out, err := exec.Command("nvidia-smi", "-i", "0",
		"--query-gpu=utilization.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]))
	if err != nil {
		return -1
	}
	return n
}

// smoothUtil folds one gpuUtil sample into an EMA (~3-4 samples of memory), mirroring
// coolerlink's smoothing so both ends of the theme flip state together — and so a
// threshold-straddling load can't flap the pumps (each flip is a device write).
func smoothUtil(ema float64, u int) float64 {
	if u < 0 {
		return ema
	}
	return 0.7*ema + 0.3*float64(u)
}

// Colorer is any aquapx device that can be set to a solid colour.
type Colorer interface {
	SetColor(r, g, b byte) error
}

// RunStatus drives the given devices from GPU load: white when idle, red when any GPU is above
// StatusGPUThreshold. It writes ONLY on an idle<->busy transition (a handful of writes an hour),
// because each write is a config-report write — with NoSave set on the devices it also skips
// the EEPROM commit, so there's no flash wear from status updates. Blocks; run in a goroutine
// or as a daemon. A nil device is skipped, so it works with either device present.
func RunStatus(interval time.Duration, devs ...Colorer) {
	busy := false
	first := true
	ema := 0.0
	for {
		u := gpuUtil()
		ema = smoothUtil(ema, u)
		nowBusy := ema >= StatusGPUThreshold
		if first || nowBusy != busy {
			c := StatusIdle
			state := "idle"
			if nowBusy {
				c, state = StatusBusy, "busy"
			}
			for _, d := range devs {
				if d == nil {
					continue
				}
				if err := d.SetColor(c[0], c[1], c[2]); err != nil {
					log.Printf("status: set colour failed: %v", err)
				}
			}
			log.Printf("status: %s (gpu %d%%) -> #%02x%02x%02x", state, u, c[0], c[1], c[2])
			busy, first = nowBusy, false
		}
		time.Sleep(interval)
	}
}

// RunStatusUntil is RunStatus with a cancellation channel, for the plugin's "System Status"
// lighting mode (selecting another mode closes stop and the loop exits).
func RunStatusUntil(stop <-chan struct{}, interval time.Duration, devs ...Colorer) {
	busy, first := false, true
	ema := 0.0
	for {
		u := gpuUtil()
		ema = smoothUtil(ema, u)
		nowBusy := ema >= StatusGPUThreshold
		if first || nowBusy != busy {
			c, state := StatusIdle, "idle"
			if nowBusy {
				c, state = StatusBusy, "busy"
			}
			for _, d := range devs {
				if d == nil {
					continue
				}
				if err := d.SetColor(c[0], c[1], c[2]); err != nil {
					log.Printf("status: set colour failed: %v", err)
				}
			}
			log.Printf("status: %s (gpu %d%%) -> #%02x%02x%02x", state, u, c[0], c[1], c[2])
			busy, first = nowBusy, false
		}
		select {
		case <-stop:
			return
		case <-time.After(interval):
		}
	}
}
