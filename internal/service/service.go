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
	"log"
	"os"
	"sync"
	"time"

	dsv1 "aquapx/gen/coolercontrol/device_service/v1"
	mv1 "aquapx/gen/coolercontrol/models/v1"
	"aquapx/internal/aqua"
)

// Server implements dsv1.DeviceServiceServer for both Aquacomputer RGB devices.
type Server struct {
	dsv1.UnimplementedDeviceServiceServer
	mu    sync.RWMutex
	dev   *aqua.D5Next
	hfn   *aqua.HighFlowNext
	ready chan struct{}
	start time.Time

	// statusStop cancels the running status loop (the "System Status" lighting mode, which
	// drives both devices from GPU load). nil when the mode isn't active.
	statusStop chan struct{}
}

// New builds a Server with no devices yet; call SetDevice/SetHFN once they're open.
func New() *Server { return &Server{ready: make(chan struct{}), start: time.Now()} }

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
	d, f := s.dev, s.hfn
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
	if len(devs) == 0 {
		return
	}
	log.Printf("status mode: driving %d device(s) from GPU load (white idle / orange busy)", len(devs))
	go aqua.RunStatusUntil(stop, 3*time.Second, devs...)
}

const (
	deviceID = "aquapx-d5next"
	hfnID    = "aquapx-highflownext"
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

// ListDevices reports the D5 Next with a single lighting channel.
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
	channels := map[string]*mv1.ChannelInfo{
		"lighting": {
			Label:   strp("Pump RGB"),
			Options: &mv1.ChannelInfo_LightingModes{LightingModes: lm},
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
	return &dsv1.ListDevicesResponse{Devices: devs}, nil
}

// InitializeDevice is a no-op (the pump needs no handshake for lighting).
func (s *Server) InitializeDevice(_ context.Context, _ *dsv1.InitializeDeviceRequest) (*dsv1.InitializeDeviceResponse, error) {
	return &dsv1.InitializeDeviceResponse{}, nil
}

// Status has no sensors to report here (the kernel driver already surfaces pump sensors).
func (s *Server) Status(_ context.Context, _ *dsv1.StatusRequest) (*dsv1.StatusResponse, error) {
	return &dsv1.StatusResponse{}, nil
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
	log.Printf("lighting: device=%s mode=%s colors=%d", req.GetDeviceId(), st.GetMode(), len(st.GetColors()))
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
