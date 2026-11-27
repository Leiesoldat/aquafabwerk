package aqua

import (
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// hwmonRoot is where the kernel exposes hardware monitoring sensors (a var for tests).
var hwmonRoot = "/sys/class/hwmon"

// cpuHwmon lists CPU temperature drivers and the label of their package/die reading, in order
// of preference ("" = temp1_input).
var cpuHwmon = []struct{ name, label string }{
	{"k10temp", "Tctl"},
	{"zenpower", "Tdie"},
	{"coretemp", "Package id 0"},
}

// hwmonTemp returns the temperature (°C) of the first hwmon device named name whose label
// matches label (or temp1_input when label is ""), or NaN.
func hwmonTemp(name, label string) float64 {
	dirs, _ := filepath.Glob(filepath.Join(hwmonRoot, "hwmon*"))
	for _, d := range dirs {
		n, err := os.ReadFile(filepath.Join(d, "name"))
		if err != nil || strings.TrimSpace(string(n)) != name {
			continue
		}
		input := filepath.Join(d, "temp1_input")
		if label != "" {
			labels, _ := filepath.Glob(filepath.Join(d, "temp*_label"))
			input = ""
			for _, l := range labels {
				b, err := os.ReadFile(l)
				if err == nil && strings.TrimSpace(string(b)) == label {
					input = strings.TrimSuffix(l, "_label") + "_input"
					break
				}
			}
			if input == "" {
				input = filepath.Join(d, "temp1_input")
			}
		}
		b, err := os.ReadFile(input)
		if err != nil {
			continue
		}
		milli, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			continue
		}
		return float64(milli) / 1000
	}
	return math.NaN()
}

// CPUTemp returns the CPU package temperature (°C), or NaN if no known sensor is found.
func CPUTemp() float64 {
	for _, c := range cpuHwmon {
		if t := hwmonTemp(c.name, c.label); !math.IsNaN(t) {
			return t
		}
	}
	return math.NaN()
}

// GPUTemp returns GPU 0's temperature (°C) from nvidia-smi, else the first amdgpu's edge
// sensor, or NaN.
func GPUTemp() float64 {
	out, err := exec.Command("nvidia-smi", "-i", "0",
		"--query-gpu=temperature.gpu", "--format=csv,noheader,nounits").Output()
	if err == nil {
		if t, err := strconv.ParseFloat(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), 64); err == nil {
			return t
		}
	}
	return hwmonTemp("amdgpu", "edge")
}

// FarbwerkSolidTemp is the constant fed to the solid-colour software sensor. Its value doesn't
// matter (both gradient colours are equal); it only keeps the sensor "set".
const FarbwerkSolidTemp = 25.0

// FarbwerkSensors returns the software sensor values aquapx feeds the Farbwerk: 1 = CPU,
// 2 = GPU, 3 = not set, 4 = constant (carries solid colours).
func FarbwerkSensors() [4]float64 {
	return [4]float64{CPUTemp(), GPUTemp(), math.NaN(), FarbwerkSolidTemp}
}

// RunFarbwerkSensors feeds the Farbwerk's software sensors (FarbwerkSensors) once a second
// until stop is closed, like aquasuite does. The GPU is queried every 5 s (nvidia-smi is a
// process spawn); the CPU every second.
func RunFarbwerkSensors(f *Farbwerk, stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	gpu := GPUTemp()
	var tick int
	failing := false
	for {
		if tick%5 == 0 && tick > 0 {
			gpu = GPUTemp()
		}
		tick++
		temps := [4]float64{CPUTemp(), gpu, math.NaN(), FarbwerkSolidTemp}
		if err := f.SetSoftwareSensors(temps); err != nil {
			if !failing {
				log.Printf("farbwerk: software sensors: %v", err)
			}
			failing = true
		} else {
			failing = false
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
