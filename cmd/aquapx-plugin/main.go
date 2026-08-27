// Command aquapx-plugin is a CoolerControl device-service plugin that exposes the
// Aquacomputer D5 Next pump as a lighting device (RGB control from the CoolerControl UI).
// coolercontrold launches it and connects over the Unix socket below.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	dsv1 "aquapx/gen/coolercontrol/device_service/v1"
	"aquapx/internal/aqua"
	"aquapx/internal/service"
)

func main() {
	sock := os.Getenv("AQUAPX_SOCK")
	if sock == "" {
		sock = "/run/aquapx.sock"
	}
	_ = os.Remove(sock)

	// Listen + serve immediately; open the pump in the background so coolercontrold's launch
	// window can't kill us before the device is ready (matches coolerlink's listen-first).
	svc := service.New()
	lis, err := net.Listen("unix", sock)
	if err != nil {
		log.Fatalf("listen on %s: %v", sock, err)
	}
	_ = os.Chmod(sock, 0o660)

	srv := grpc.NewServer()
	dsv1.RegisterDeviceServiceServer(srv, svc)

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		<-c
		log.Println("shutting down")
		svc.CloseDevice()
		srv.GracefulStop()
	}()

	// Open the flow meter first (best-effort, no retry loop): ListDevices only advertises it
	// once it's set, and the pump goroutine below is what unblocks ListDevices.
	if f, err := aqua.OpenHighFlowNext(); err == nil {
		svc.SetHFN(f)
		log.Println("High Flow Next opened; lighting channel published")
	} else {
		log.Printf("High Flow Next not available: %v (continuing without it)", err)
	}

	go func() {
		for attempt := 1; ; attempt++ {
			d, err := aqua.OpenD5Next()
			if err == nil {
				svc.SetDevice(d)
				log.Println("D5 Next opened; lighting channel published")
				return
			}
			log.Printf("open D5 Next attempt %d failed: %v; retrying in 3s", attempt, err)
			time.Sleep(3 * time.Second)
		}
	}()

	log.Printf("aquapx device-service listening on %s", sock)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
