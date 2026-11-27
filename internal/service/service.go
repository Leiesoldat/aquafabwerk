// Package service implements CoolerControl's gRPC DeviceService for aquapx, exposing the
// Aquacomputer D5 Next pump as a lighting device. CoolerControl connects over a Unix socket,
// discovers the device + its single lighting channel via ListDevices, and drives the RGB via
// the Lighting RPC (which does a read-modify-write of the pump's config report).
//
// Note: the D5 Next also appears in CoolerControl as a *sensor* device via the kernel
// aquacomputer_d5next driver; this plugin adds the lighting it can't do. Listen-first
// (serve immediately, open the device in the background) so coolercontrold's launch window
// never kills us before the device is ready.
package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	dsv1 "aquapx/gen/coolercontrol/device_service/v1"
	mv1 "aquapx/gen/coolercontrol/models/v1"
	"aquapx/internal/aqua"
)

// Server implements dsv1.DeviceServiceServer for the Aquacomputer RGB devices (D5 Next,
// High Flow Next, LEAKSHIELD, Farbwerk).
type Server struct {
	dsv1.UnimplementedDeviceServiceServer
	mu    sync.RWMutex
	dev   *aqua.D5Next
	hfn   *aqua.HighFlowNext
	ls    *aqua.Leakshield
	fw    *aqua.Farbwerk
	ready chan struct{}
	start time.Time

	// statusStop cancels the running status loop (the "System Status" lighting mode, which
	// drives both devices from GPU load). nil when the mode isn't active.
	statusStop chan struct{}

	// pumpBright caches the pump ring's brightness (0..100%) for Status, so CoolerControl's
	// polling doesn't read the config report every second. -1 until first read or set.
	brightMu   sync.Mutex
	pumpBright int
}

// New builds a Server with no devices yet; call SetDevice/SetHFN once they're open.
func New() *Server { return &Server{ready: make(chan struct{}), start: time.Now(), pumpBright: -1} }

// SetDevice publishes the opened pump and unblocks waiters.
func (s *Server) SetDevice(d *aqua.D5Next) {
	s.mu.Lock()
	s.dev = d
	s.mu.Unlock()
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

// SetHFN publishes the opened flow meter.
func (s *Server) SetHFN(f *aqua.HighFlowNext) {
	s.mu.Lock()
	s.hfn = f
	s.mu.Unlock()
}

// SetLeakshield publishes the opened LEAKSHIELD.
func (s *Server) SetLeakshield(l *aqua.Leakshield) {
	s.mu.Lock()
	s.ls = l
	s.mu.Unlock()
}

// SetFarbwerk publishes the opened Farbwerk and starts feeding its software sensors (CPU and
// GPU temperature, plus the constant that carries solid colours) once a second.
func (s *Server) SetFarbwerk(f *aqua.Farbwerk) {
	s.mu.Lock()
	s.fw = f
	s.mu.Unlock()
	go aqua.RunFarbwerkSensors(f, make(chan struct{}))
}

func (s *Server) getFW() *aqua.Farbwerk {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fw
}

func (s *Server) getLS() *aqua.Leakshield {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ls
}

func (s *Server) getDev() *aqua.D5Next {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dev
}

func (s *Server) getHFN() *aqua.HighFlowNext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hfn
}

func (s *Server) waitDev(d time.Duration) *aqua.D5Next {
	select {
	case <-s.ready:
	case <-time.After(d):
	}
	return s.getDev()
}

// CloseDevice releases both device handles (so a restart doesn't leave them held).
func (s *Server) CloseDevice() {
	if d := s.getDev(); d != nil {
		_ = d.Close()
	}
	if f := s.getHFN(); f != nil {
		_ = f.Close()
	}
	if l := s.getLS(); l != nil {
		_ = l.Close()
	}
	if f := s.getFW(); f != nil {
		_ = f.Close()
	}
}

func strp(s string) *string { return &s }

// stopStatus cancels the status loop if it's running. Caller must hold s.mu.
func (s *Server) stopStatusLocked() {
	if s.statusStop != nil {
		close(s.statusStop)
		s.statusStop = nil
	}
}

// startStatus (re)starts the GPU-load status loop driving both devices. Devices are put in
// NoSave mode first so the periodic recolours never hit the EEPROM.
func (s *Server) startStatus() {
	s.mu.Lock()
	if s.statusStop != nil { // already running (the mode is set on both devices) — one loop drives both
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.statusStop = stop
	d, f, l := s.dev, s.hfn, s.ls
	s.mu.Unlock()

	var devs []aqua.Colorer
	if d != nil {
		d.NoSave = true
		devs = append(devs, d)
	}
	if f != nil {
		f.NoSave = true
		devs = append(devs, f)
	}
	if l != nil {
		l.NoSave = true
		devs = append(devs, l)
	}
	if len(devs) == 0 {
		return
	}
	log.Printf("status mode: driving %d device(s) from GPU load (white idle / orange busy)", len(devs))
	go aqua.RunStatusUntil(stop, 3*time.Second, devs...)
}

const (
	deviceID = "aquapx-d5next"
	hfnID    = "aquapx-highflownext"
	lsID     = "aquapx-leakshield"
	fwID     = "aquapx-farbwerk"
	// fwAllChannel turns every Farbwerk output off (brightness 0) or back on.
	fwAllChannel = "All"

	// D5 Next channel IDs. CoolerControl shows a lighting channel's ID rather than its label,
	// so the IDs use Aquacomputer's names: "Pump" for the pump's LED ring and "RGBpx LED" for
	// the RGBpx output header.
	pumpChannel  = "Pump"
	stripChannel = "RGBpx LED"
	// pumpBrightChannel is a 0..100% "fan-style" channel that sets the D5 Next's master
	// brightness (byte 237, verified on hardware to dim both the pump ring and the RGBpx LEDs):
	// CoolerControl's lighting protocol has no brightness setting of its own.
	pumpBrightChannel = "brightness"
)

// Health confirms the service is up.
func (s *Server) Health(_ context.Context, _ *dsv1.HealthRequest) (*dsv1.HealthResponse, error) {
	return &dsv1.HealthResponse{
		Name:          "aquapx",
		Version:       "0.1.0",
		Status:        dsv1.HealthResponse_STATUS_OK,
		UptimeSeconds: uint64(time.Since(s.start).Seconds()),
	}, nil
}

// ListDevices reports the D5 Next (pump ring + RGB output strip channels) and any other
// Aquacomputer devices found.
func (s *Server) ListDevices(_ context.Context, _ *dsv1.ListDevicesRequest) (*dsv1.ListDevicesResponse, error) {
	_ = s.waitDev(30 * time.Second)
	lm := &mv1.LightingModes{}
	for _, m := range aqua.LightingModes() {
		fn := m.Label
		lm.LightingMode = append(lm.LightingMode, &mv1.LightingModes_LightingMode{
			Name:         m.Name,
			FrontendName: &fn,
			MinColors:    m.MinColors,
			MaxColors:    m.MaxColors,
		})
	}
	slm := &mv1.LightingModes{}
	for _, m := range aqua.StripLightingModes() {
		fn := m.Label
		slm.LightingMode = append(slm.LightingMode, &mv1.LightingModes_LightingMode{
			Name:         m.Name,
			FrontendName: &fn,
			MinColors:    m.MinColors,
			MaxColors:    m.MaxColors,
		})
	}
	channels := map[string]*mv1.ChannelInfo{
		pumpChannel: {
			Label:   strp(pumpChannel),
			Options: &mv1.ChannelInfo_LightingModes{LightingModes: lm},
		},
		stripChannel: {
			Label:   strp(stripChannel),
			Options: &mv1.ChannelInfo_LightingModes{LightingModes: slm},
		},
		pumpBrightChannel: {
			Label: strp("D5 Next Brightness"),
			Options: &mv1.ChannelInfo_SpeedOptions{SpeedOptions: &mv1.SpeedOptions{
				MinDuty: 0, MaxDuty: 100, FixedEnabled: true,
			}},
		},
	}
	devs := []*mv1.Device{{
		Id:   deviceID,
		Name: "Aquacomputer D5 Next (RGB)",
		Info: &mv1.DeviceInfo{
			Channels: channels,
			Model:    strp("D5 Next"),
			DriverInfo: &mv1.DriverInfo{
				Name:    strp("aquapx"),
				Version: strp("0.1.0"),
			},
		},
	}}
	// High Flow Next: only its colour is decoded (its onboard flow-indicator effect keeps
	// running), so it advertises a single "static" colour mode.
	if s.getHFN() != nil {
		hlm := &mv1.LightingModes{}
		fn := "Colour"
		sn := "System Status (match fans)"
		hlm.LightingMode = append(hlm.LightingMode,
			&mv1.LightingModes_LightingMode{Name: "static", FrontendName: &fn, MinColors: 1, MaxColors: 1},
			&mv1.LightingModes_LightingMode{Name: "status", FrontendName: &sn, MinColors: 0, MaxColors: 0},
		)
		devs = append(devs, &mv1.Device{
			Id:   hfnID,
			Name: "Aquacomputer High Flow Next (RGB)",
			Info: &mv1.DeviceInfo{
				Channels: map[string]*mv1.ChannelInfo{"lighting": {
					Label:   strp("Flow Meter RGB"),
					Options: &mv1.ChannelInfo_LightingModes{LightingModes: hlm},
				}},
				Model: strp("High Flow Next"),
				DriverInfo: &mv1.DriverInfo{
					Name:    strp("aquapx"),
					Version: strp("0.1.0"),
				},
			},
		})
	}
	// LEAKSHIELD: single colour, all 16 onboard effects (decoded from aquasuite captures),
	// off (brightness 0) and the shared status mode.
	if s.getLS() != nil {
		llm := &mv1.LightingModes{}
		for _, m := range aqua.LeakshieldLightingModes() {
			fn := m.Label
			llm.LightingMode = append(llm.LightingMode, &mv1.LightingModes_LightingMode{
				Name:         m.Name,
				FrontendName: &fn,
				MinColors:    m.MinColors,
				MaxColors:    m.MaxColors,
			})
		}
		devs = append(devs, &mv1.Device{
			Id:   lsID,
			Name: "Aquacomputer LEAKSHIELD (RGB)",
			Info: &mv1.DeviceInfo{
				Channels: map[string]*mv1.ChannelInfo{"lighting": {
					Label:   strp("LEAKSHIELD RGB"),
					Options: &mv1.ChannelInfo_LightingModes{LightingModes: llm},
				}},
				Model: strp("LEAKSHIELD"),
				DriverInfo: &mv1.DriverInfo{
					Name:    strp("aquapx"),
					Version: strp("0.1.0"),
				},
			},
		})
	}
	// Farbwerk: one lighting channel per RGB output ("RGB Output 1".."RGB Output 4", which
	// CoolerControl title-cases to "Rgb Output N"; rename in CoolerControl) plus "All", which
	// turns every output off or back on.
	if s.getFW() != nil {
		fwlm := &mv1.LightingModes{}
		for _, m := range aqua.FarbwerkLightingModes() {
			fn := m.Label
			fwlm.LightingMode = append(fwlm.LightingMode, &mv1.LightingModes_LightingMode{
				Name:         m.Name,
				FrontendName: &fn,
				MinColors:    m.MinColors,
				MaxColors:    m.MaxColors,
			})
		}
		chs := map[string]*mv1.ChannelInfo{}
		for n := 1; n <= 4; n++ {
			id := fmt.Sprintf("RGB Output %d", n)
			chs[id] = &mv1.ChannelInfo{
				Label:   strp(id),
				Options: &mv1.ChannelInfo_LightingModes{LightingModes: fwlm},
			}
		}
		alm := &mv1.LightingModes{}
		for _, m := range aqua.FarbwerkAllModes() {
			fn := m.Label
			alm.LightingMode = append(alm.LightingMode, &mv1.LightingModes_LightingMode{
				Name: m.Name, FrontendName: &fn, MinColors: m.MinColors, MaxColors: m.MaxColors,
			})
		}
		chs[fwAllChannel] = &mv1.ChannelInfo{
			Label:   strp(fwAllChannel),
			Options: &mv1.ChannelInfo_LightingModes{LightingModes: alm},
		}
		devs = append(devs, &mv1.Device{
			Id:   fwID,
			Name: "Aquacomputer Farbwerk (RGB)",
			Info: &mv1.DeviceInfo{
				Channels: chs,
				Model:    strp("Farbwerk"),
				DriverInfo: &mv1.DriverInfo{
					Name:    strp("aquapx"),
					Version: strp("0.1.0"),
				},
			},
		})
	}
	return &dsv1.ListDevicesResponse{Devices: devs}, nil
}

// InitializeDevice is a no-op (the pump needs no handshake for lighting).
func (s *Server) InitializeDevice(_ context.Context, _ *dsv1.InitializeDeviceRequest) (*dsv1.InitializeDeviceResponse, error) {
	return &dsv1.InitializeDeviceResponse{}, nil
}

// Status reports the brightness channel's current value as its duty. There are no sensors
// here (the kernel driver already surfaces the pump's sensors).
func (s *Server) Status(_ context.Context, req *dsv1.StatusRequest) (*dsv1.StatusResponse, error) {
	if req.GetDeviceId() != deviceID {
		return &dsv1.StatusResponse{}, nil
	}
	dev := s.getDev()
	if dev == nil {
		return &dsv1.StatusResponse{}, nil
	}
	s.brightMu.Lock()
	pct := s.pumpBright
	s.brightMu.Unlock()
	if pct < 0 {
		p, err := dev.Brightness()
		if err != nil {
			return &dsv1.StatusResponse{}, nil
		}
		s.brightMu.Lock()
		s.pumpBright, pct = p, p
		s.brightMu.Unlock()
	}
	duty := float64(pct)
	return &dsv1.StatusResponse{Status: []*mv1.Status{{
		Id:     pumpBrightChannel,
		Metric: &mv1.Status_Speed{Speed: &mv1.Status_FanSpeed{Duty: &duty}},
	}}}, nil
}

// EnableManualFanControl is a no-op: the brightness channel needs no mode switch.
func (s *Server) EnableManualFanControl(_ context.Context, _ *dsv1.EnableManualFanControlRequest) (*dsv1.EnableManualFanControlResponse, error) {
	return &dsv1.EnableManualFanControlResponse{}, nil
}

// FixedDuty sets the D5 Next's brightness (pump ring and RGBpx LEDs) from the "D5 Next
// Brightness" channel (0..100%).
func (s *Server) FixedDuty(_ context.Context, req *dsv1.FixedDutyRequest) (*dsv1.FixedDutyResponse, error) {
	log.Printf("fixed duty: device=%s channel=%s duty=%d", req.GetDeviceId(), req.GetChannelId(), req.GetDuty())
	if req.GetDeviceId() != deviceID || req.GetChannelId() != pumpBrightChannel {
		return &dsv1.FixedDutyResponse{}, nil
	}
	dev := s.getDev()
	if dev == nil {
		return &dsv1.FixedDutyResponse{}, nil
	}
	pct := int(req.GetDuty())
	if err := dev.SetBrightness(pct); err != nil {
		return nil, err
	}
	s.brightMu.Lock()
	s.pumpBright = aqua.BrightnessByteToPercent(aqua.BrightnessPercentToByte(pct))
	s.brightMu.Unlock()
	return &dsv1.FixedDutyResponse{}, nil
}

// Lighting applies a lighting mode + base colour from the CoolerControl UI to the pump.
func (s *Server) Lighting(_ context.Context, req *dsv1.LightingRequest) (*dsv1.LightingResponse, error) {
	st := req.GetSetting()
	if st == nil {
		return &dsv1.LightingResponse{}, nil
	}
	var r, g, b byte
	if cs := st.GetColors(); len(cs) > 0 {
		r, g, b = byte(cs[0].GetR()), byte(cs[0].GetG()), byte(cs[0].GetB())
	}
	log.Printf("lighting: device=%s channel=%s mode=%s colors=%d", req.GetDeviceId(), req.GetChannelId(), st.GetMode(), len(st.GetColors()))
	// Farbwerk outputs are independent of the other devices and of the status loop.
	if req.GetDeviceId() == fwID {
		f := s.getFW()
		if f == nil {
			return &dsv1.LightingResponse{}, nil
		}
		ch := strings.ToLower(req.GetChannelId())
		if ch == strings.ToLower(fwAllChannel) {
			var err error
			switch st.GetMode() {
			case "off":
				err = f.AllOff(aqua.FarbwerkStatePath())
			case "on":
				err = f.AllOn(aqua.FarbwerkStatePath())
			default:
				err = fmt.Errorf("unknown Farbwerk All mode %q", st.GetMode())
			}
			if err != nil {
				return nil, err
			}
			return &dsv1.LightingResponse{}, nil
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(ch, "rgb "), "output %d", &n); err != nil {
			return nil, fmt.Errorf("unknown Farbwerk channel %q", req.GetChannelId())
		}
		var c1, c2 *[3]byte
		cs := st.GetColors()
		if len(cs) > 0 {
			c1 = &[3]byte{byte(cs[0].GetR()), byte(cs[0].GetG()), byte(cs[0].GetB())}
		}
		if len(cs) > 1 {
			c2 = &[3]byte{byte(cs[1].GetR()), byte(cs[1].GetG()), byte(cs[1].GetB())}
		}
		if err := f.Apply(n, st.GetMode(), c1, c2); err != nil {
			return nil, err
		}
		return &dsv1.LightingResponse{}, nil
	}
	// The D5 Next's RGBpx LED output is independent of the pump ring and the status loop.
	// (Any other D5 Next channel, including "Pump", falls through to the pump ring below.)
	if req.GetDeviceId() == deviceID && req.GetChannelId() == stripChannel {
		dev := s.getDev()
		if dev == nil {
			return &dsv1.LightingResponse{}, nil
		}
		if err := dev.StripApplyLighting(st.GetMode(), r, g, b); err != nil {
			return nil, err
		}
		return &dsv1.LightingResponse{}, nil
	}
	// "status" on either device drives BOTH from GPU load, matching the iCUE fans.
	if st.GetMode() == "status" {
		s.startStatus()
		return &dsv1.LightingResponse{}, nil
	}
	// Any explicit colour/effect takes over: stop the status loop and restore persistence.
	s.mu.Lock()
	s.stopStatusLocked()
	s.mu.Unlock()
	if d := s.getDev(); d != nil {
		d.NoSave = false
	}
	if f := s.getHFN(); f != nil {
		f.NoSave = false
	}
	if l := s.getLS(); l != nil {
		l.NoSave = false
	}
	if req.GetDeviceId() == lsID {
		l := s.getLS()
		if l == nil {
			return &dsv1.LightingResponse{}, nil
		}
		if err := l.ApplyLighting(st.GetMode(), r, g, b); err != nil {
			return nil, err
		}
		return &dsv1.LightingResponse{}, nil
	}
	if req.GetDeviceId() == hfnID {
		f := s.getHFN()
		if f == nil {
			return &dsv1.LightingResponse{}, nil
		}
		if err := f.SetColor(r, g, b); err != nil {
			return nil, err
		}
		return &dsv1.LightingResponse{}, nil
	}
	dev := s.getDev()
	if dev == nil {
		return &dsv1.LightingResponse{}, nil
	}
	if err := dev.ApplyLighting(st.GetMode(), r, g, b); err != nil {
		return nil, err
	}
	return &dsv1.LightingResponse{}, nil
}

// ResetChannel is a no-op success.
func (s *Server) ResetChannel(_ context.Context, _ *dsv1.ResetChannelRequest) (*dsv1.ResetChannelResponse, error) {
	return &dsv1.ResetChannelResponse{}, nil
}

// Shutdown lets CoolerControl stop the plugin cleanly.
func (s *Server) Shutdown(_ context.Context, _ *dsv1.ShutdownRequest) (*dsv1.ShutdownResponse, error) {
	log.Println("shutdown requested by CoolerControl")
	dev := s.getDev()
	go func() {
		time.Sleep(150 * time.Millisecond)
		if dev != nil {
			_ = dev.Close()
		}
		os.Exit(0)
	}()
	return &dsv1.ShutdownResponse{}, nil
}
